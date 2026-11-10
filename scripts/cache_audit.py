#!/usr/bin/env python3
"""cache_audit.py — 按输入大小审计上游前缀缓存的命中率，并拆开「冷启动」与「暖会话」。

背景（issue #256 那类报告的根因）：只看「各输入档位的平均命中率」会把两件事混在
一起，读出来的结论通常是错的：

  1. 冷启动成本 —— 新会话第一跳没有前缀可复用，必然 miss。输入越小，冷启动在一次
     请求里占的比例越高，所以小档的平均命中率**天然**低（20% 上下不是故障）。
  2. 暖会话被打断 —— 同一会话的后续轮本该命中 95%+。它掉下来才是故障：会话指纹
     每轮变化（例如取「整个 body 前 64KiB」算哈希，对话不到 64KiB 时最新一轮也被
     算进去）→ 每轮换账号 / 换 cache 槽 → 前缀缓存全废。

本脚本把两件事分开算，并给出「暖会话打断率」这个能直接定性的指标：
同一 (client_ip, user_agent, model) 上相邻两跳、prompt 只小幅增长 = 同一会话继续；
其中上一跳命中 >=90% 而这一跳 <50% = 暖会话被打断。

判据参考（部署后验收）：
  - 各档「续轮」命中率都应 >=95%；80k+ 应 >=98%
  - 暖会话打断率应 <1%
  - 各档「全量」命中率低但续轮高 = 冷启动占比高，不是故障
  - 长会话（>=10 跳）里出现 <80% 的，逐个查是不是换号或 prompt_cache_key 漂移

用法：
  python3 scripts/cache_audit.py /opt/workbuddy2api-panel/data/request-logs
  ssh ubuntu@host 'sudo -n python3 -' < scripts/cache_audit.py   # 直接把脚本灌过去
（只读 JSONL，不需要 panel token，也不改任何文件。）
"""
import collections
import glob
import json
import os
import sys

BUCKETS = [(0, 16_000, "<16k"), (16_000, 40_000, "16-40k"),
           (40_000, 80_000, "40-80k"), (80_000, 10**9, "80k+")]


def bucket_of(prompt_tokens):
    for lo, hi, name in BUCKETS:
        if lo <= prompt_tokens < hi:
            return name
    return "?"


def load(dirpath):
    files = sorted(glob.glob(os.path.join(dirpath, "requests-*.jsonl")))
    if not files:
        raise SystemExit("没有找到 requests-*.jsonl：%s" % dirpath)
    out = []
    for f in files:
        with open(f, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    e = json.loads(line)
                except Exception:
                    continue
                pt = int(e.get("prompt_tokens") or 0)
                ch = int(e.get("cache_hit_tokens") or 0)
                cm = int(e.get("cache_miss_tokens") or 0)
                if pt <= 0 or ch + cm == 0:
                    continue          # 没有用量/缓存字段的（非 chat、或上游没给 usage）不参与
                out.append({
                    "t": str(e.get("time") or ""),
                    "ip": e.get("client_ip") or "",
                    "ua": (e.get("user_agent") or "")[:24],
                    "model": e.get("model") or "",
                    "acct": e.get("account") or "",
                    "pt": pt, "hit": ch, "den": ch + cm,
                })
    out.sort(key=lambda x: x["t"])
    return files, out


def main():
    if len(sys.argv) < 2:
        raise SystemExit("用法: cache_audit.py <request-logs 目录>")
    files, ev = load(sys.argv[1])
    print("files = %d (%s .. %s)" % (len(files), os.path.basename(files[0]),
                                     os.path.basename(files[-1])))
    print("events = %d" % len(ev))
    if not ev:
        return

    # 续轮标记：同一 (ip, ua, model) 上相邻两跳、prompt 小幅增长 = 同一会话继续。
    # 阈值 15%/4k 取的是「比一轮正常增长宽、比换会话窄」的经验值；两条都放宽会让
    # 不同会话被误并成一条（把冷启动算进续轮，反而掩盖故障）。
    for i in range(1, len(ev)):
        a, b = ev[i - 1], ev[i]
        b["cont"] = False
        if (a["ip"], a["ua"], a["model"]) != (b["ip"], b["ua"], b["model"]):
            continue
        grow = b["pt"] - a["pt"]
        if 0 < grow < max(4000, a["pt"] * 0.15):
            b["cont"] = True
    ev[0]["cont"] = False

    n = collections.Counter(); hit = collections.Counter(); den = collections.Counter()
    cn = collections.Counter(); chit = collections.Counter(); cden = collections.Counter()
    for e in ev:
        b = bucket_of(e["pt"])
        n[b] += 1; hit[b] += e["hit"]; den[b] += e["den"]
        if e["cont"]:
            cn[b] += 1; chit[b] += e["hit"]; cden[b] += e["den"]

    print()
    print("%-8s %8s %9s %10s %9s %9s" % ("bucket", "turns", "all%", "continuation", "cont%", "cold%"))
    for _, _, b in BUCKETS:
        if not den[b]:
            continue
        cold_hit, cold_den = hit[b] - chit[b], den[b] - cden[b]
        print("%-8s %8d %8.1f%% %10d %8.1f%% %8.1f%%" % (
            b, n[b], 100.0 * hit[b] / den[b], cn[b],
            100.0 * chit[b] / cden[b] if cden[b] else 0,
            100.0 * cold_hit / cold_den if cold_den else 0))

    # 暖会话打断率：上一跳 >=90%，这一跳 <50%
    warm = broken = switched = 0
    for a, b in zip(ev, ev[1:]):
        if not (a["hit"] / a["den"] >= 0.9):
            continue
        grow = b["pt"] - a["pt"]
        if not (0 < grow < max(4000, a["pt"] * 0.15)):
            continue
        if (a["ip"], a["ua"], a["model"]) != (b["ip"], b["ua"], b["model"]):
            continue
        warm += 1
        if b["hit"] / b["den"] < 0.5:
            broken += 1
            if a["acct"] != b["acct"]:
                switched += 1
    print()
    print("暖会话续跳 = %d，被打断 = %d (%.2f%%)，其中换号 %d" % (
        warm, broken, 100.0 * broken / warm if warm else 0, switched))
    if warm and broken / warm > 0.01:
        print("  ↑ 超过 1%：查会话指纹是否每轮变化（换号 = 粘性键漂移；同号 = cache 槽漂移）")

    # 长会话（>=10 跳）命中率分布：这里出现低值一定有问题，冷启动稀释解释不了
    runs = []
    cur = []
    for e in ev:
        if cur and e["cont"] and (cur[-1]["ip"], cur[-1]["ua"], cur[-1]["model"]) == (e["ip"], e["ua"], e["model"]):
            cur.append(e)
        else:
            if cur:
                runs.append(cur)
            cur = [e]
    if cur:
        runs.append(cur)
    dist = collections.Counter()
    long_n = 0
    worst = []
    for r in runs:
        if len(r) < 10:
            continue
        long_n += 1
        h = sum(x["hit"] for x in r[1:]); d = sum(x["den"] for x in r[1:])
        pct = 100.0 * h / d if d else 0
        key = ">=95%" if pct >= 95 else "80-95%" if pct >= 80 else "50-80%" if pct >= 50 else "<50%"
        dist[key] += 1
        if pct < 80:
            worst.append((pct, len(r), r[0]["ua"], r[0]["model"]))
    print("长会话（>=10 跳）= %d，命中分布 %s" % (long_n, dict(dist)))
    for pct, ln, ua, model in sorted(worst)[:5]:
        print("  低命中长会话: %.1f%%  %d 跳  %s  %s" % (pct, ln, ua, model))


if __name__ == "__main__":
    main()
