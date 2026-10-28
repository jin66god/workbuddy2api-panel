# Changelog

本项目的版本变更记录。更早版本的发布说明见
[GitHub Releases](https://github.com/jin66god/workbuddy2api-panel/releases)。

## v1.21.9（2026-10-28）

### 修复

- 补齐 login 侧两处 CN Origin：`internal/panel/login.go` 与 `internal/loginsite`
  （站点 CN Origin）同步改为 `https://www.workbuddy.cn`；billing/school 的 **API 主机**
  仍为 codebuddy.cn（那是接口域名，5.9.3 未变）。v1.21.8 只改了 headers.go，登录链路漏改。

## v1.21.8（2026-10-28）

### 修复

- **11-128 补齐第二层抢救「搬家」**：降级期不再直接整体替换成中性提示词（会丢掉客户端人设/项目约定），
  改为先把 system 原文搬进首条 user、原 system 位置放中性提示词——一个字符都不丢；搬不动
  （无 system 文本/多模态 parts/无 user）才退回整体替换。至此形成三层：拆句（结构）→ 搬家（保人格）→ 替换（兜底）。
- **对齐 WorkBuddy 5.9.3 客户端契约（三个确定值）**：CN 客户端版本 `5.8.3`→`5.9.3`，
  内置 CLI `2.161.0`→`2.165.0`，CN Origin/Referer `codebuddy.cn`→`workbuddy.cn`
  （5.9.3 真机 661 条带 Origin 请求零使用 codebuddy.cn，旧值属 CodeBuddy CLI 时代遗留）。

## v1.21.7（2026-10-28）

### 修复

- **快过期积分优先在会话粘性路径失效**：新会话哈希分配的候选只做 healthy/inFlight
  过滤，既没有「快过期硬优先」，也漏掉积分保底，导致加权随机 + 粘性下可能把收费模型
  分给触底号、快过期号只拿到 3 槽概率。现拆分两条口径：`StickyAssignableForModelRealm`
  （healthy + 保底 + 在途 + 快过期硬优先）只用于新会话分配；`WeightedAvailableUIDsForModelRealm`
  （加保底、不含硬优先）用于已有绑定校验，避免健康绑定号被硬优先踢掉导致会话抖动与前缀缓存击穿。
- **`expiringNow` 增加 `credits > 0` 校验**：0 余额号不再被判为「快过期」而优先选中，避免无谓的上游 402 与硬冷却。
- **工具 schema 的 enum 缺 `type` 时补推断类型**：Kimi 系对参数 schema 严格校验，只写 enum
  不写 type 会直接 400 `11133`；能无损推断才补（全串/全整数/含小数/全布尔），混合标量或含 null 保持原样。

### 新增

- **上游模型目录改版监控（watchdog，默认关）**：定期对网关 `modelList()` 取排序后 sha256 指纹，
  变化写日志；首轮只建基线，零新增上游调用。配置 `watchdog.enabled` / `watchdog.interval_seconds`（默认 3600）。
- **默认监听改为回环**：`Default()` 的 listen 从 `:7863` 改为 `127.0.0.1:7863`，避免空/弱 `api_key`
  与面板直接暴露公网；`docker-compose.yml` 显式 `WB2A_LISTEN=0.0.0.0:7863` 保证容器端口映射可用。

## v1.21.6（2026-10-28）

### 修复

- **DSH 客户端 400 `code=11-128`（Illegal API invocation from an unapproved channel）**：
  上游把 DSH 0.3.x 系统提示里说明「`@` 开头路径怎么解析」的那整段原文当作渠道特征，
  单发首句放行、多句连排即拦截，导致 DSH 的每次请求都被拒。现对该段做整段同义改写
  （`@` 路径语义全部保留：相对路径按 workspace 解析、结尾斜杠表示目录、其余按文件读、
  `@"..."` 用于含空格的路径），只改措辞让连续特征失配；同时保留「首句插词」兜底规则，
  覆盖措辞再次变化的情况。新旧两种线上措辞均已覆盖。

## v1.21.3（2026-10-26）

### 修复

- **运行日志「错误汇总」**：把日志环里的 WARN/ERR 按「账号 × 类型」聚合，按
  bad → warn → info（同级按次数降序）排序，附最近时刻与可悬停查看全文的样本，
  一眼看出是某个号在狂报错还是普遍性抖动。新增 `GET /panel/api/error_summary`。
- **添加账号站点顺序与文案对齐官方口径**：国内版 → 国际版 → 国际版（旧桌面端），
  并标注各站域名；说明补齐「国际版登录后自动完成注册地区、激活与试用额度领取」
  与「旧桌面端仅用于已注册在 workbuddy.ai 的老账号」。上一版把顺序改错并擅自发版，
  本版一并纠正。

## v1.21.1（2026-10-26）

### 变更

- **国内版对话主域切到 `www.workbuddy.cn`**：官方桌面端 `product.json` 的 endpoint 就是它，
  `copilot.tencent.com` 只是 `NetworkFailover` 里的备选入口，长期拿备选当主入口是可判别的
  形态差异；且 copilot 对海外出口 TLS 握手会直接复位。两条域实测 `/v3/config`、billing 汇总、
  对话空体返回一致；token 刷新路径也已验证同构（空 refreshToken 返回同样的 `code=10001`），
  不会因换域导致刷新失效。需要回退时在 `cn.chat_base` 填 `https://copilot.tencent.com`。
- **面板添加账号说明补齐域分工**：国内版对话 `www.workbuddy.cn`、登录 Origin `www.codebuddy.cn`、
  成长任务 `www.workbuddy.cn`，不再只标一个 `codebuddy.cn` 让人以为 `workbuddy.cn` 被去掉。

## v1.21.0（2026-10-26）

### 新增

- **授权链接二维码**：添加账号弹窗把授权链接渲染成二维码，手机/异地添加时不必再转发链接
  （IM 预览抓取、`&state=` 被截断的问题绕开）。二维码编码器补齐版本 6（上限 134 字节），
  链接超出容量时自动回退为只显示链接。
- **`server.shutdown_timeout`**：优雅关停窗口可配，默认 30s。长流式请求不再被旧的固定
  5 秒掐成连接重置；超时会打 WARN 说明在途请求被放弃。
- **用量落盘健康度**：`/panel/api/usage` 新增 `persist_last_error` 与 `persist_failures`，
  磁盘满/目录只读时能看出「已 N 轮没写成功」。

### 修复

- **手动全量任务防重入**：签到/旅行/活跃/保活/余额共用一把闸门，重复触发返回 409，
  不再因为双击跑两轮全量、把每号上游请求翻倍。
- **用量落盘失败不再丢数据**：写盘失败后恢复 dirty 标记，下个周期继续重试；此前失败即
  跳过后续每轮 flush，进程崩溃会丢掉一整个防抖周期。
- **签到已签判定窄化**：不再把 `code=400` 与裸 `checkin` 当成「已签到」，真实失败
  （参数缺失/风控/活动未开启）不会被静默吞掉；幂等码改用 `code=10001`。
- **登录轮询过期提示**：`login poll` 遇到非信封 4xx 明确提示 state 可能已过期、需重新
  获取授权链接；token 响应缺少 accessToken 单独报错，不再与「还没登录完」混为一谈。
- **账号状态迁移留痕**：禁用终态、连败降权、熔断触发各打一条 WARN（含原因、截止时刻、
  退避轮次），账号被移出候选集后能回溯发生了什么。

### 性能

- **请求归档查询文件级短路**：带时间区间的查询按文件名日期跳过无交集整档，不再解析
  整个归档目录（上限 100MB，且被面板轮询放大）。
- **粘性绑定冷启动恢复**：Upstash `LoadBinds` 由逐 key GET 改为 SCAN + 分批 MGET，
  往返次数从 N+1 收敛为常数次；恢复不完整会落 WARN 而不是静默截断。
- **模型目录短缓存**：`/panel/api/models` 增加 60 秒缓存（只缓存零错误响应），
  「重新获取」按钮带 `refresh=1` 强制重探。

## v1.20.3（2026-10-26）

### 修复

- **12153 计数不再丢失**：`NoteSessionDead` 递增、`ClearSessionDead` 清零都置脏落盘，
  避免重启后计数回退导致误杀刚自证的账号。
- **粘性 touch 改 CAS**：竞争窗口里不再复活已失败的账号、不再冲掉粘性跟随。
- **备注保存不再谎报成功**：浏览器存储写入失败时如实报错。
- **「积分与 token 按小时」表恢复渲染**：此前只有表头、永远空表。

### 安全

- **弱 api_key + 非回环监听启动告警**：默认密钥暴露到公网时第一时间提示。

### 其他

- 登录链路 `ClientVersion` 对齐官方 CLI 2.176.0（UA 与授权链接 version 参数同源）。

## v1.20.2（2026-10-25）

### 修复

- **6020「排队等待」不再被当成账号限流**：此前只把 6021「队列已满」判为渠道级排队，
  6020 会落进 429 兜底、单次即整号软冷却 600s，同号其它模型一并不可调度。现在两个
  业务码同判 `upstream_busy`（不罚号、不轮转，短等后重试）。
- **refresh token 失效不再等同账号不可用**：access token 仍未过期时上游对话依然可用
  （生产实测部分账号 refresh 恒回 12153、AT 还剩数十天、对话全 200）。此前发请求前的
  预刷新失败会**一次就永久禁用**该账号；现在继续用现有 AT 服务，保活与余额刷新也不再
  把这种号计入会话失效，AT 真正过期后仍按原语义处置。
- **模型锁因与解锁时刻归因**：`ModelBlockStatus.Reason` 改为绑定最早解封的那个账号
  （此前可能与 `Until` 来自不同账号）；模型级冷却 reason 保留上游原文，面板与
  `gateway_hint` 能直接看到「使用量已超出频率限制，将在 … 重置」。

### 改进

- **裸模型名落错域时给出可操作提示**：池里只有另一域账号时，`no_healthy_account` 的
  `gateway_hint` 改为 `model "x" is only available in the global realm; retry with "global:x"`，
  不再只让人去查永远健康的 `/status`。
- **周期任务人味化**：余额刷新与补领清扫的间隔在 ±15% 内稳定偏移（余额刷新 255~345s、
  清扫 765~1035s），余额刷新同时把各账号请求在窗口内摊开，消除「整点全池齐射」的机械
  节拍。`schedule.balance_refresh_jitter_permill` / `schedule.task_sweep_jitter_permill`
  默认 `150`（写 `0` 恢复固定间隔），面板「配置 → 排程」可直接改。
- **夜间任务参与上限**：`schedule.night_account_cap`（默认 `0` = 不限）>0 时夜猫子只让
  积分最高的前 N 个账号参加，其余号夜里静默；白天签到 / 旅行 / 成长仍全池参与。

## v1.20.1（2026-10-24）

### 文档

- 面板「出站 User-Agent」占位提示改为实际默认值：留空时按账号身份自动
  （CN `WorkBuddy/5.8.3`、global `workbuddy-ai/5.8.4`，CLI 段 `2.161.0`）。
  原提示写的 `CLI/2.63.2` 是历史遗留值，会让人误以为出站 UA 是另一套形态。
- README 清理过期版本标注（「当前版本」提示、历史 PR 合并记录、Go 版本、
  镜像 tag 示例），版本信息统一以 Releases 与本文档为准。

## v1.20.0（2026-10-24）

### 新增

- **三个登录站点**：面板「添加账号」与 `login.sh` 都支持国内站（`cn`）、官方国际站
  （`intl`，`www.codebuddy.ai`）、旧桌面端国际版（`workbuddy`，`www.workbuddy.ai`）。
  国际站登录后自动完成注册地区、激活与试用额度领取，并跳过 CN 签到。
  `login.sh` 新增 `--site=`，`--realm=global` 保留为兼容写法（映射到 workbuddy）。
- **国际域按凭据 `domain` 选主机**：`*.codebuddy.ai` 打 codebuddy.ai，
  `*.workbuddy.ai` 打 workbuddy.ai。两个国际域同后端，但 token 只在签发域可用；
  `global.chat_base` / `billing_base` 仍可手工覆盖。已有账号的凭据行为不变。

### 修复

- **模型级冷却完整对齐上游重置墙钟**：6004/6000 冷却不再被 `soft_rate_max`
  （默认 2h）截断。模型级冷却只停单个模型，按上游给的时刻恢复；账号级软冷却的
  封顶逻辑保持不变。
- **429 `code=6000` 按模型级限流处理**：仅当文案同时包含「切换其他模型」时放宽为
  模型级；不带该提示的 6000 仍按账号级处理，避免把整号受限误判成单模型限流。
- **`expiresAt` 毫秒自动归一**：导入官方 CLI 凭据（毫秒时间戳）时不再算出公元
  58800 年的到期时间，token 会正常刷新，不会在到期后静默全 401。
- **登录终态许可错误立即报错**：`12005`（席位满）/ `11212`（许可证过期）/
  `11216`（试用到期）直接把上游文案返回面板并停止轮询，不再空转到会话 TTL；
  `11217` / `12151` 继续轮询。
- **模型未调工具的正常收尾降为 INFO**：合法回合不再以 WARN 进入日志巡检。

### 变更

- 登录链路 UA 与授权链接 `version` 参数对齐官方 CLI `2.174.0`（仅登录链路；
  chat 侧 UA 仍按账号身份分档，不受影响）。

## v1.19.0（2026-10-24）

### 新增

- **按模型查询可用容量**：新增 `GET /v1/capacity?model=<cn:|global:>模型名`，返回
  该模型此刻可出票的账号数（`model_available_accounts`）与整池健康账号数
  （`available_accounts`）。纯本地台账、不打上游，供负载均衡/客户端判断某模型在
  本实例是否可服务；被 `models.hidden` 点名的模型恒报 0。鉴权与 `/status` 同口径。
- **保活临期窗口**：周期 token 保活不再每天对全部账号轮换 refresh token，只刷新
  剩余有效期不超过窗口（默认 7 天）的凭证；已过期/无到期时间的账号始终刷新。
  面板「全部保活」手动入口仍全量。上游对 refresh token 有反重放，减少无谓轮换
  同时降低风控暴露面。

### 修复

- **`reasoning_effort=off/none` 现在真的关闭思考**：此前 `off` 只是档位排序占位，
  会被降级管线抬成最弱正档，表现为「要求关闭思考却仍返回思维链」。DeepSeek 系
  改写为 `thinking.type=disabled` 并清除档位字段；其他模型清除非法档位值后透传。
- **网关 401 不再误杀健康账号**：refresh 失败时先分辨是「上游应用层明确判词」
  还是「边缘拦截/抖动」。只有明确判词（4xx 非 429、非 HTML 拦截页）才累计
  session dead；HTML 401/403、5xx、429、传输错误只软冷却。chat 与搜索两条路径
  同口径；刷新成功后仍 401 的降级凭证也不再计死。

### 配置

- `schedule.keepalive_refresh_window`（默认 `168h`）：周期保活的临期窗口，Go 时长
  格式；留空回落默认，非法或 `<=0` 启动报错。面板保存后热生效。

## v1.18.0（2026-10-23）

### 新增

- **运行日志持久化**：面板「运行日志」的 sys / task / chat 三频道同时写入
  `<state>/logs/panel-YYYY-MM-DD.jsonl`，进程启动时回填最近 500 行。此前只有
  内存环形缓冲，更新面板/重启后历史日志全部消失；现在重启与升级后仍可查看。
  保留策略与请求归档同口径：默认 7 天、总量 50 MiB，超出按最旧删除。
- **运行日志导出**：运行日志标题栏新增「导出」按钮与范围选择（今日 / 近三天 /
  7 天 / 全部），下载 ZIP（`panel-logs.ndjson` + `meta.json`）。导出前统一脱敏
  token / api_key / Bearer / 手机号；单次上限 200 MiB，超限返回 413 并提示缩短
  范围；同一时刻只允许一个导出任务。

### 配置

- `logging.panel_log_persist_enabled`（默认 `true`）：运行日志落盘开关
- `logging.panel_log_retention_days`（默认 `7`）：保留天数
- `logging.panel_log_max_mb`（默认 `50`）：落盘总量上限
- `logging.log_export_max_mb`（默认 `200`）：单次导出上限

## v1.17.8（2026-10-23）

### 修复

- **图片请求检测容错**：`hasImagePart` 改为逐条解析 `content`，字符串 / null 形态
  不再让整个请求体解析失败，避免另一条消息里的 `image_url` 被漏判（错误提示会因此
  误指向「纯文本请求」）。
- **14003 等待不再提前醒来**：等待时长由双向抖动（±25%）改为只向上浮动
  （0 ~ +25%）。此前抽到负向系数会在模型冷却未解除时醒来，把「每请求只等一次」
  的额度白花掉，客户端多等一轮后仍拿 503。
- **轮转耗尽也报 `model_busy`**：末端补一次模型阻塞快照。此前走轮转耗尽 / WAF
  止损路径时不经过选号失败分支，同一件「全池该模型繁忙」会退化成 429
  `rate_limit_exceeded` 且没有 Retry-After（`MaxRotate=1` 时必现）。
- **账号摊开按计划时刻遍历**：修正 uid 序下「前面的号计划更晚、把后面的号逐个堵住」
  导致的执行时刻塌缩（多个账号挤在同一秒跑完）。

## v1.17.7（2026-10-23）

### 修复

- **401 网关拒绝后的自愈**：上游网关（APISIX）在 token 被提前吊销时返回
  `401 Authorization Required` HTML，没有业务信封。此前这类响应归入通用 4xx：
  对话请求只换号、余额周期任务只打日志，既不会刷新 token，也不会标记账号失效，
  表现为「全部账号 401、只能人工重登」，且余额任务每 5 分钟无限空刷。
  现在单独分类为 `token_rejected`：对话与余额刷新各自**强制刷新一次**并用新
  token 重试同一账号；刷新失败才按连续 3 次会话失效软着陆禁用，单次 401 不杀号。
  搜索端点的 401 语义不唯一（可能是该号未开通搜索），只换号不累计会话失效。
- **企业切换会话不再阻断成长任务**：`token_source=enterprise_switch` 只记录本次
  授权走了企业切换流程，不代表账号处于企业上下文。成长任务门控改为只看
  `enterpriseId` 与静态 API Key，登录日志降为提示。修复微信扫码登录的个人号
  「成长任务无法执行」的问题。

## v1.17.6（2026-10-22）

### 安全

- 升级到 Go 1.27.3，修复 **CVE-2026-111943**（`crypto/x509` 对 8 个私钥外泄的
  eMudhra / emSign 中间 CA 取消信任）。验证时会拒绝包含这些中间证书的证书链，
  防止中间人攻击。`go.mod` 的 `go` 指令改为 `1.27.3`，Docker 构建镜像
  改为 `golang:1.27.3-alpine`，CI 通过 `go-version-file: go.mod` 自动使用 1.27.3。
  该过滤为硬编码、不可关闭，须重新编译才能生效。

## v1.17.5（2026-10-22）

### 修复

- 镜像构建失败：uTLS 依赖要求 Go ≥ 1.24，Dockerfile 构建镜像由 `golang:1.22-alpine`
  升级到 `golang:1.24-alpine`，修复 `go mod download` 退出码 1 导致的双架构镜像构建失败。

## v1.17.4（2026-10-22）

### 新增

- **出站 TLS 指纹对齐官方客户端**：18 个 cipher 顺序、12 个扩展顺序、9 项签名算法
  全部按真机抓包复刻，直连 HTTPS 走 uTLS 自定义 ClientHello。
  实测 `JA3=1a3153f314dc13e133dc71d113a81b16`、`JA4=t13d1812h1_5d04281c6031_ef7df7f74e48`，
  与官方业务 API 逐位一致；`WB2_TLS_FINGERPRINT=0` 可回退标准库。

### 变更

- 镜像仅在打 tag 时构建，日常合并到 main 不再触发双架构构建。
- Release 日志只保留面向用户的更新说明。

## v1.17.3（2026-10-22）

### 修复

- **出站标识整体全面对齐官方 UUID 形状**：养号/活动上报链路里发往上游的
  `conversationId`、`requestId`、`traceId`、`rootRequestId` 与自动化任务名此前
  带 `wb2api-` 前缀或「前缀 + 毫秒时间戳」形状，属于可被上游风控按固定串或
  ID 形状批量筛选的第三方特征。现在统一由 `internal/upstream/official_ids.go`
  生成官方形状标识：
  - `conversationId` / `requestId` → UUID 形状（与前端口 `randomUUID` 一致）
  - `traceId` → 32 位十六进制
  - 定时任务名 → 官方产品名，不再携带中转标识
- **桌面端 `X-Conversation-ID` / `X-Request-ID`**：去掉 `wb2api-conv-<纳秒>`
  前缀与纯纳秒数字，改为官方形状，桌面专家对话请求不再自报家门。
- **命中范围**（全部出站，均已替换）：`upstream/desktop.go`、`upstream/school.go`、
  `upstream/blackcat.go`、`panel/autotask.go`（14 处）、`scheduler/scheduler.go`、
  `scheduler/travel.go`。
- 明确保留**不出站**的同名标识：`chatcmpl-wb2api`（下游 SSE id）、`msg_wb2api_`、
  Redis 前缀、哈希盐、临时文件名与测试数据，避免误改影响协议兼容。

### 验证

- `go build ./...` 通过；`go test ./internal/upstream ./internal/scheduler ./internal/panel` 全绿。
- 出站 grep 复核：`internal/`、`cmd/` 内除注释与 import 路径外无 `wb2api` 出站字符串。
