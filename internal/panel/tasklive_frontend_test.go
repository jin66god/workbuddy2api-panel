// tasklive_frontend_test.go 实时任务卡的结构断言（静态，不需要浏览器）。
//
// 为什么用静态断言：这三类缺陷都不会让编译或 node 语法检查报错——
//   - 卡片没插进任务中心视图（元素存在于文件里但不在那个 section 内）；
//   - 控件排在 app.js 之后（顶层绑定拿到 null，点了没反应）；
//   - go() 没挂钩（进页面不拉数据，用户以为功能没做）。
package panel

import (
	"strings"
	"testing"
)

func TestLiveTasksFrontendWiring(t *testing.T) {
	html := string(indexHTML)
	js := string(appJS)

	// ① 卡片必须在任务中心视图区间内，且在成长任务队列之前（作为顶部卡片）。
	sec := strings.Index(html, `id="view-taskscenter"`)
	if sec < 0 {
		t.Fatal("index.html 缺 view-taskscenter")
	}
	for _, id := range []string{`id="liveList"`, `id="liveEvery"`, `id="btnLiveReload"`, `id="liveSummary"`, `id="liveEmpty"`} {
		at := strings.Index(html, id)
		if at < 0 {
			t.Fatalf("index.html 缺 %s", id)
		}
		if at < sec {
			t.Fatalf("%s 排在任务中心视图之前", id)
		}
	}
	if live, queue := strings.Index(html, `id="liveList"`), strings.Index(html, `id="qcSummary"`); live > queue {
		t.Fatalf("实时任务卡应排在成长任务队列之前（live=%d queue=%d）", live, queue)
	}

	// ② 控件必须排在 app.js 之前：app.js 解析到脚本标签就会执行顶层绑定，
	//    排在脚本之后的新控件在绑定时还是 null，onclick 会被静默跳过。
	if i, s := strings.Index(html, `id="btnLiveReload"`), strings.Index(html, `src="app.js"`); i < 0 || s < 0 || i > s {
		t.Fatalf("btnLiveReload 必须出现在 app.js 之前（btn=%d script=%d）", i, s)
	}

	// ③ 前端实现与后端端点名对齐，且 go() 里挂上启停钩子。
	for _, want := range []string{
		"function startLive(",
		"function stopLive(",
		"function loadLive(",
		"function renderLive(",
		"function liveTaskRow(",
		"'tasks/live'",
		"if (v === 'taskscenter') startLive(); else stopLive();",
		"claim: '可领取'",
	} {
		if !strings.Contains(js, want) {
			t.Fatalf("app.js 缺 %q", want)
		}
	}

	// ④ 轮询间隔必须被后端 TTL 抬起：比 TTL 短只是打服务端缓存。
	if !strings.Contains(js, "Math.max(liveEvery, ttl)") {
		t.Fatal("轮询间隔没有按 ttl_sec 取下限")
	}
	// ⑥ 卡片必须落在 taskscenter 这个 section 内部（漏在 </section> 之后等于不可见）。
	if end := strings.Index(html[sec:], "</section>"); end < 0 || strings.Index(html, `id="liveList"`) > sec+end {
		t.Fatal("实时任务卡不在任务中心 section 内")
	}
	// ⑦ 轮询间隔必须对未初始化的 liveTTL 兜底，否则 Math.max 得到 NaN、等同 0ms 紧循环。
	if !strings.Contains(js, "liveTTL > 0 ? liveTTL : 45") {
		t.Fatal("轮询间隔缺少 liveTTL 未初始化兜底")
	}
	// ⑤ 切到后台标签页要停：否则多开页面等于对上游做压测。
	if !strings.Contains(js, "document.visibilityState === 'visible'") {
		t.Fatal("轮询没有按页面可见性暂停")
	}
}

// TestLiveTasksDivBalance 实时任务卡所在区间 div 配平。
func TestLiveTasksDivBalance(t *testing.T) {
	html := string(indexHTML)
	start := strings.Index(html, "<!-- 实时任务：")
	if start < 0 {
		t.Fatal("找不到实时任务卡的起始注释")
	}
	end := strings.Index(html[start:], "<!-- 后台任务")
	if end < 0 {
		// 回退到下一个 box 的 header 文案（成长任务队列）
		end = strings.Index(html[start:], `<h3>成长任务队列`)
		if end < 0 {
			t.Fatal("找不到实时任务卡的结束锚点")
		}
	}
	span := html[start : start+end]
	// 切片终点落在「下一个 box 的开标签之后」，那段没有对应的收尾标签；
	// 因此再收到最后一个 </div> 处，只量实时任务卡自己的容器。
	last := strings.LastIndex(span, "</div>")
	if last < 0 {
		t.Fatal("实时任务卡区间内没有收尾标签")
	}
	block := span[:last+len("</div>")]
	open, closed := strings.Count(block, "<div"), strings.Count(block, "</div>")
	if open != closed {
		t.Fatalf("实时任务卡 div 不配平：开 %d 闭 %d", open, closed)
	}
	if open == 0 {
		t.Fatal("实时任务卡区间没量到任何容器")
	}
}
