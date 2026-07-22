# Fork 与上游功能差异维护台账

本文用于记录 `DoodleXu/sub2api` fork 相对上游官方仓库 `Wei-Shaw/sub2api` 的定制功能差异，方便后续同步上游、迭代和 debug。

最后更新：2026-07-22

## 当前对比基线

| 项目 | 当前值 | 说明 |
| --- | --- | --- |
| Fork 远端 | `origin = DoodleXu/sub2api` | 当前工作主线 |
| 上游远端 | `upstream = Wei-Shaw/sub2api` | 官方原版仓库 |
| Fork 同步前 HEAD | `6171eccb2 fix: 修复发版安全与静态检查` | 本次合并 v0.1.162 前的 fork 基线 |
| 当前已合并上游 release 基线 | `refs/tags/upstream/v0.1.162` -> `27f094e096` | v0.1.162 已合入本 fork，fork 发布版本提升为 `0.1.226` |
| 上游最新 release 基线 | `refs/tags/upstream/v0.1.162` -> `27f094e096` | 2026-07-20 发布的官方最新非草稿 release |
| 上游 main HEAD | `5a8d6c4e41` | 本次同步时的远端 main，包含 v0.1.162 后续提交；未越过 release 标签合并 |
| fork 相对上游 release 差异 | fork 仍保留自定义功能差异 | 本次共处理 31 个冲突路径（含 4 个 modify/delete）；继续保留 fork 聚合文件结构和六类核心定制行为，并迁入上游入口 IP 安全、图片存储热配置、Grok 工具缓存、OpenAI/Codex 与订阅展示修复 |

更新本文时建议先刷新引用：

```bash
git fetch origin --prune
git fetch upstream refs/heads/main:refs/remotes/upstream/main --no-tags
git fetch upstream refs/tags/v0.1.162:refs/tags/upstream/v0.1.162 --force
git log --oneline --right-only --cherry-pick refs/tags/upstream/v0.1.162^{}...HEAD
git diff --name-status refs/tags/upstream/v0.1.162^{}..HEAD
```

如上游 release tag 更新，先把 `v0.1.162` 替换为新的官方 release tag，再更新本节。

本次 `v0.1.162` 合并说明：

- 入口安全吸收可信代理、转发客户端 IP 自定义 header、API Key IP 列表局部更新、请求头读取超时与大小限制；fork 继续保持 `trusted_proxies: []` 和“不默认信任转发 IP”的安全默认值，不执行上游把历史 false 自动翻为 true 的兼容迁移。
- 管理端图片存储支持运行时读取和更新 S3 配置；动态 uploader 已接入 fork 异步图片任务的原子终态、待确认对象清单、失败补偿、周期清理和重启恢复，单次任务始终使用同一 uploader 快照，避免热切换时跨存储清理。
- OpenAI/Codex 吸收 Responses 生图能力筛选、模型发现、首轮 failover、缓存计费与 SSE 热路径优化；Grok 吸收客户端工具缓存、免费额度配置、视频代理与配额刷新，并继续保留 fork Responses Lite、Agent Identity 脱敏、首图 TTFT 和每轮图片计费快照。
- 运营邮件报告补齐结构化摘要变量和中英文模板；订阅剩余天数、有效期复数、计划币种及管理员计划列表展示已更新，同时保留 fork 按订阅类型显示日/周/月额度的规则。
- 更新检查支持 GitHub Token，在线更新/回滚请求超时与服务端限制对齐；S3 secret 继续拒绝进入备份导出，pnpm 11 的 `form-data@<4.0.6` 安全覆盖已迁到实际生效的 workspace 配置。
- 账号工具菜单补齐自动倍率探测的中英文标题、间隔、成功和失败文案；标签容器允许收缩换行，开关、输入框和保存按钮固定不收缩，修复原始 i18n key 撑宽菜单并把控件挤出可视区域的问题。
- 上游 API Key 计费倍率探测新增 New API 兼容回退：原生 `/v1/sub2api/billing` 返回 404/405 时，以同一 API Key 只读查询 `/api/log/token`，从最新有效消费日志提取实际 `group_ratio`；样本时间决定新鲜度，旧日志不会因轮询续期，无日志明确显示“暂无计费样本”，前端将该值标记为消费日志观测值而非上游实时声明。
- 合并后全面审核补强动态图片存储与订阅币种兼容：后台生命周期天数可由环境变量和管理端完整读写；图片与备份 S3 设置读取异常不再回落并覆盖已存密钥，备份 S3/记录/定时配置读取失败不再被吞没或覆盖历史数据，备份 S3 更新会通知复用它的图片 uploader 失效，瞬时读取或建连失败按有界间隔自动重试；任务创建时固定 uploader，热关闭或切换存储后仍在原存储完成上传与补偿，关闭期间的清理登记受控且超时任务会释放 uploader 快照。历史空币种统一迁移为 `CNY` 并同步数据库默认值，管理端恢复币种编辑并默认人民币；同时将 `golang.org/x/text` 升级至 `v0.39.0`，消除 `GO-2026-5970` 非法输入死循环风险；部署文档同步保持“不默认信任转发 IP”的 fork 安全口径。
- Grok 保留 fork 的显式工具语义：Free OAuth 的纯客户端函数工具缓存默认关闭，只有账号开关、请求头或严格 Claude Desktop 指纹才允许加入缓存路由工具；API Key 原生 Chat Completions 不再携带 OAuth CLI 身份头，Chat→Responses 在 `instructions` 已声明 JSON 时不重复插入 developer 消息。
- Ops Monitoring 的“生图 Avg”固定使用绿色展示，不复用普通 TTFT 红黄阈值；生图本身是长耗时任务，该数值继续用于观察真实首图平均等待时间，不作为常规首 Token 延迟告警状态。
- 4 个上游拆分文件的增量已移植回 fork 聚合模块，没有恢复已删除的拆分文件；签到、运营中心、人民币成本、账号归档、Web 创作台和生图管理入口均保留。

本次 `v0.1.159` + `v0.1.160` + `v0.1.161` 合并说明：

- 安全面迁入 API Key 凭证长度限制、鉴权查询并发保护、负缓存失效 outbox、无效鉴权滥用限流和入口拒绝聚合；session binding 与敏感操作 step-up 按官方 v0.1.161 改为默认关闭，避免升级后隐式改变既有会话和管理流程。
- 新增官方 Prompt Audit 后端协调器、迁移、管理 API、路由与前端工作台，并接入 Responses、Chat Completions、Messages、WebSocket、Alpha Search 和媒体提交入口；继续保留 fork 风控中心、内容审核与 cyber 会话屏蔽链路。
- OpenAI Responses/WS 吸收流式 content part/full output、模型级临时冷却和完整生命周期控制；fork 每 turn 图片计费快照、首图耗时、图片计数、Agent Identity 请求级脱敏和 Responses Lite 行为继续保留。
- Grok 合入受保护媒体访问、图片/视频 endpoint 规范化、媒体资格探测、Free 账号缓存判定与加密内容恢复；纯 function tools 不再隐式注入搜索工具，避免改变模型工具选择。
- 管理订阅支持过期记录原地续期并重置周期用量；运营设置改为启动预热、请求热路径只读内存快照，管理更新立即刷新快照。
- 前端运行时同时加载 fork 单体语言包与上游模块化语言包，模块化键覆盖旧键；补齐中英文键对称测试，修复账号上游声明倍率标题、未探测状态和 Prompt Audit 文案显示原始 key 的问题。
- 部署吸收 BuildKit 原生架构构建、Go/pnpm 缓存和 Redis 持久化参数换行修复，同时保留 `ghcr.io/doodlexu/sub2api`、`UPDATE_REPOSITORY=DoodleXu/sub2api` 与 fork 镜像来源标签。
- 合并后审核修复 Prompt Audit Worker 长时间复用旧配置的问题：每次领取任务前重新读取活动配置，并在原子 claim 时记录实际执行版本；关闭审计、缩减 worker 或切换节点后不再继续用旧快照清空积压队列。异步图片任务恢复扫描覆盖尚未产生对象清单的 `processing` 记录，进程重启后会按执行超时转失败；提交入口增加单实例与单 API Key 有界准入，默认上限分别为 128 和 8，超限返回 429。
- 发版门禁修复上游合并遗留的内容审核与 Grok media 未使用兼容 wrapper，并将 Axios 从 `1.16.0` 升级到 `1.18.1`，消除 `GHSA-gcfj-64vw-6mp9` 高危代理继承漏洞；不通过新增或延期安全例外绕过扫描。
- 冲突解决继续使用 fork 聚合模块承载上游拆分文件语义，没有恢复已删除的 10 个拆分文件；签到、运营中心、人民币成本、账号归档、Web 创作台和生图管理入口均保留。

本次 `v0.1.157` + `v0.1.158` 合并说明：

- 安全与管理面迁入上游 session binding、管理员 step-up 2FA、操作审计日志与保留周期设置；前端保留 fork 运营中心入口，并新增独立 `/admin/audit-logs` 路由和侧栏项，避免与 `/admin/ops`、`/admin/operations` 混用。
- 账号、分组和渠道监控模板复制能力已接入；账号列表同时保留 fork 的归档/取消归档和批量归档操作，并加入上游 API Key 计费倍率探测、自动探测设置、单账号/批量探测和可信度提示。
- OpenAI Responses 吸收 Agent Identity、invalid task 单次恢复、显式拒绝字段逐项重试、API Key 5xx/413 failover、错误响应脱敏、首输出等待、拼接 JSON 修复和 `response.failed` 调度语义；fork 的 Responses Lite `additional_tools`、字符串 arguments、JSON mode、生图归档与首图 TTFT 字段继续保留。
- WS v2 合并畸形事件拒绝、终态尾随文档连接隔离、握手/终态模型级瞬时冷却和 idle close；同时继续保留 fork 每 turn 图片计费快照、`image_first_output_ms`、归档输入和 direct relay 生命周期绑定。
- Grok OAuth 自定义 base URL 现按上游 v0.1.158 行为生效，图片/视频媒体请求仍按 endpoint 类型切换官方 API；健康状态对账要求测试夹具和真实账号同时具备 active/schedulable、refresh token 与未过期 token，避免陈旧账号状态静默复用。
- 图片计费新增 `image_input_tokens` / `image_input_cost`，后端 usage 提取、计费和前端明细展示已接入；fork 的人民币成本、实际成本、图片归档和 Ops 生图 Avg 口径保持不变。
- 上游异步图片任务、S3 存储、订阅币种、审计日志和分组复制迁移与服务均已接入；迁移 runner 按完整文件名记录，因此 fork 已存在的同数字前缀迁移不需要重编号。
- 冲突解决继续使用 fork 聚合模块承载上游拆分文件语义，没有恢复已被 fork 删除的聚合拆分文件；`VERSION` 保持 `0.1.223`，部署仍维持 `linux/amd64` + GHCR 约束。

历史 `v0.1.155` 合并说明：

- 上游 Admin UI 请求级 Server-Timing 已合入，默认关闭；启用后通过响应头展示 total/app/db/redis/dependency 耗时。fork 的 Ops 动态直方图与生图 TTFT 聚合继续保留，两者分别观察“单次后台请求内部耗时”和“历史网关请求延迟”，不共享持久化口径。
- OpenAI 长上下文计费改为账号级布尔开关且默认关闭，字段、迁移、API、CRS 同步、影子账号继承和 usage log 审计标记已移植进 fork 聚合文件；人民币成本小时聚合与实际成本口径保持不变。
- Codex models manifest 采用上游 API Key 自定义上游、短期缓存与账号故障转移，同时保留 fork 的 `TryCodexModels` 回退，避免无可用 manifest 账号时破坏客户端内置模型列表。
- 原生 Responses namespace 的请求展平与响应还原覆盖 HTTP、passthrough 和 WSv2；fork 继续保留 Responses Lite `additional_tools`、function arguments 字符串、JSON 大整数、JSON mode 与 hosted 生图隔离行为。
- Grok Responses 官方转发现已接入 fork 聚合入口，并吸收 `reasoning.content=null` 清洗、滚动 24 小时免费额度、Web SSO 批量导入、OAuth 新账号探活与监控中心 Grok 支持；文生视频继续按上游规则把无输入图片的 `grok-imagine-video-1.5` 改写为 `grok-imagine-video`，图生视频则保留 1.5 模型。
- 生图链路加入非流式 JSON keepalive、流式最终状态修正与客户端 Lite 图片工具保留；fork 的生图并发槽、异步归档队列、Web 创作台恢复态和管理端资产安全访问继续保留。
- 调度器全量重建合并、账号/代理到期事件处理、系统日志 host 筛选、HTTP/2 keepalive PING、reset credits 检测和 `/v1/messages` 精确映射修复已合入。
- 冲突解决未恢复 `usage_log_repo_insert.go`、`admin_account.go`、`openai_gateway_forward.go`、`openai_gateway_usage.go` 等上游拆分文件；相关行为均移植到 fork 现有聚合模块，减少结构漂移带来的隐性回归。
- 部署继续遵循 fork 的 `linux/amd64` + GHCR 生产约束，未恢复 Apple Container、DockerHub 或多架构发布路线；`VERSION` 继续保持 fork 的 `0.1.222`。
- merge-tree 与合并后检查确认签到、运营中心、人民币成本、账号归档、Web 创作台和生图管理核心文件仍存在；账号 OAuth 更新继续拒绝归档账号，调度与批量操作仍过滤 `archived_at`。
- 2026-07-14 fork 调整官方 Ops Monitoring 的请求时长分布：移除固定 `0-100ms` 至 `2000ms+` 桶，改为按当前所选时间窗口及平台/分组筛选结果的实际最小、最大请求时长动态生成最多 6 个对数桶；窄范围继续使用等宽桶，并为原始日志查询设置 5 秒上限，兼顾长尾辨识度和大窗口资源保护。后续同步上游若改动 `ops_repo_histograms.go` 或 `OpsLatencyChart.vue`，需保留动态量程行为。
- 2026-07-14 Ops Monitoring 的 TTFT 卡新增“生图 Avg”：原 `first_token_ms` 继续保留全部流式请求的首 token 口径，另以 `usage_logs.image_first_output_ms` 记录流式首次 partial/final 图片或非流式完整图片响应的真实可用输出时间，并统一 API Key、OAuth、Responses、HTTP passthrough 与 WS v2 direct passthrough 入口，同时排除视频请求；WS direct relay 按成功写入的 `response.create` 为每个 turn 分配序号并登记起点，收到 `response_id` 后再绑定，因此首 token、首图和 duration 均包含 `response.created` 前的上游排队时间；适配层按同一序号保存 request/upstream/image billing model、size、service tier 与 reasoning effort 快照，终态不会串用其他轮次或退化为聊天模型/default size。每个 direct passthrough `response.create` 还会保留客户端 `event_id`，缺失时注入内部唯一值；若上游以可恢复 `error.error.event_id` 拒绝该轮但不生成 `response_id`，relay 会精确撤销对应 timing 与计费快照，避免下一轮错绑。首图与最终图片数按 turn 跟踪，重复出现于 `output_item.done`/`response.completed` 的同一图片不会重复计数。小时/日预聚合保存独立样本数和加权平均值。历史行无法从 lifecycle 事件到达时间还原真实首图时间，因此保持 NULL；已提交的 `175_ops_image_generation_ttft_average.sql` 保持 checksum 不变，新增 `177_add_usage_log_image_first_output_ms.sql` 负责清理早期草稿基于 `first_token_ms` 生成的不可信聚合值，后续仅统计新产生的可信样本。

历史 `v0.1.153` 合并说明：

- Codex Responses→Chat 的 `input[].type=additional_tools` 已改由上游官方 `EffectiveResponsesTools` 统一读取，移除 fork 重复的 `ExpandResponsesLiteTools` 路径；fork 继续保留 Responses Lite 生图桥接门控、`responses_required`、custom/namespace/tool_search 回程还原和字符串形式 `web_search_call.action` 兼容。
- Grok 正式迁入 xAI API Key、OAuth prompt cache、第三方 base URL、Chat→Responses 缓存桥、限流持久化、视频编辑/延长与模型同步。`grok` 默认别名重新交由 xAI 官方映射解析为 `grok-4.5`，显式账号映射仍优先，避免 fork 旧的 `grok-4.3` 强制别名阻断新缓存桥。
- Codex `/alpha/search` 网页搜索按次计费已迁入 fork 聚合结构：分组支持 `web_search_price_per_call`，API Key auth snapshot 升至 v16，计费使用不含高峰因子的基础倍率；未恢复 `admin_group.go`、`openai_gateway_usage.go` 等已删除拆分文件。
- OpenAI WebSocket 入站会话加入按 API Key 的有界 lifecycle lease；同时吸收真实 upstream endpoint 记录、平台感知的无账号诊断、用量日期本地化和 API Key 最近使用 IP 查询索引优化。
- 部署继续遵循本 fork 的 `linux/amd64` + GHCR 生产约束。上游 Apple Container 固定依赖 `linux/arm64` 且默认使用上游镜像，因此本次未保留其脚本、文档、CI 与环境变量入口；保留手动部署 `.env` 的 `chmod 600` 加固。
- merge-tree 与合并后检查确认签到、运营中心、人民币成本、账号归档、Web 创作台和生图管理核心文件仍存在；账号调度继续过滤 `archived_at`，人民币成本小时聚合和今日实际成本字段仍保留。

历史 `v0.1.151` 合并说明：

- 上游行为修复已移植：Codex identity header pairing、Codex image generation tool strip、OpenAI Fast/Flex 用户级规则、setup-token 后台刷新、Grok `reasoning_effort` 兼容、usage request_type legacy alias 过滤。
- 冲突解决策略：不恢复 fork 已删除的上游拆分文件，保留当前 fork 文件结构，在现有模块内移植对应行为和测试。
- fork 额外修复：为 OpenAI `json_object` JSON mode 增加统一的受管请求兼容兜底，覆盖原生 API Key HTTP、Chat -> Responses、OAuth 和 WebSocket 入站；当 input 缺少 `JSON/json` 关键字时自动补最小 developer 指令，同时保留 function call `arguments` 的字符串类型和 JSON 大整数精度。raw passthrough 不做该变换。
- 生图桥接保持历史兼容：Codex hosted 生图桥接开启后，非 Responses Lite 的 HTTP 与 WebSocket 受管请求继续自动注入 `image_generation` 工具，并将顶层 `image_gen` namespace 归一为单一 hosted 工具，避免改变既有生图能力。
- 2026-07-12 选择性回迁上游 main 的 Codex 工具桥接修复：Responses→Chat fallback 支持 `custom`/`namespace`/`tool_search` 工具转换与回程还原。fork 继续对纯 `image_generation`、`web_search` 等 Responses-only hosted 工具保留 `responses_required` 保护，但混合 Codex 本地工具时允许 fallback 丢弃 hosted 工具并保留终端/SSH/MCP 工具能力。
- 2026-07-12 fork 继续补齐上游尚未覆盖的 Codex Responses Lite：`gpt-5.6-sol` 等模型将工具放在 `input[].type=additional_tools` 而非顶层 `tools`；Chat fallback 现会提取并合并这些工具、移除已消费的载体，并使用同一有效工具集合完成 custom/tool_search/namespace 回程还原与 Responses-native 资格判断，避免 macOS Codex 经 Chat 上游时丢失 `exec`/终端/MCP 工具。
- 2026-07-12 fork 对 Responses Lite 单独隔离 Codex 生图桥接：当 `input[]` 含 `type=additional_tools` 时，其中的 `image_gen` 只视为能力声明，不触发图片权限、计费或并发槽，也不自动混入顶层 `image_generation`、`tool_choice=auto` 或生图提示；仅在当前轮显式选择生图工具或最新有效输入为 `image_generation_call` 时允许桥接。非 Lite 请求继续保留历史自动桥接行为。该规则避免原生 Responses 上游忽略 Lite 载体中的 `exec`/SSH/MCP 工具：同一 `gpt-5.6-sol` 请求走账号 399 的 Chat fallback 时工具正常，走账号 1011 的原生 Responses 且命中无条件生图注入时退化为纯文本；1011 上带 Codex UA 的 8 次复现均提示无 `exec`，绕过生图桥接后同账号 4 次均返回 `custom_tool_call`。后续同步上游或调整生图能力时必须保留 Responses Lite 的协议形状门控、非 Lite 兼容行为，以及 HTTP/WebSocket 原生透传回归。

## 核心 fork 定制功能

### 1. 签到

定位：用户侧每日签到奖励、运营侧签到配置和预算控制。

主要差异：

- 新增签到奖励计算、连续签到倍率、暴击、每日/月度预算、用户月限额和预算耗尽兜底奖励。
- 支持按照实际成本或余额消费口径判断签到资格。
- 用户侧有常驻签到入口，余额明细中将签到奖励识别为独立来源。
- 运营中心可查看签到趋势、记录，并导出签到数据。

关键代码：

- `backend/internal/service/daily_checkin_service.go`
- `backend/internal/handler/daily_checkin_handler.go`
- `backend/internal/service/setting_service.go`
- `frontend/src/components/common/DailyCheckinButton.vue`
- `frontend/src/components/user/profile/ProfileCheckinCalendarCard.vue`
- `frontend/src/views/admin/OperationsCenterView.vue`
- `frontend/src/views/user/RedeemView.vue`

关键迁移：

- `backend/migrations/145_user_checkins.sql`
- `backend/migrations/149_daily_checkin_controls.sql`
- `backend/migrations/155_daily_checkin_operations.sql`
- `backend/migrations/157_daily_checkin_budget_fallback.sql`

相关提交线索：

- `3aeca6bdd 签到：接入每日奖励前端入口`
- `df65f1003 支持签到运营控制与预算统计`
- `af787aad4 feat: 增加签到运营中心`
- `598e52b9b feat: 支持签到预算保底奖励`
- `80b9376e2 fix: 修复签到率超过百分百`

同步上游注意：

- 上游如果改动设置结构、公开 settings API、余额变更记录、用户 profile 或兑换记录，要检查签到字段是否仍被完整序列化。
- 上游如果新增自己的签到/奖励体系，应优先避免并存两套奖励履约路径，统一到一个余额入账和审计模型。

### 2. 运营中心

定位：在官方 admin/ops 运维监控之外，fork 增加面向运营行为的数据看板和导出能力。

主要差异：

- 新增 `/admin/operations` 页面和后端 routes，承载运营概览、签到趋势、签到记录、数据导出等。
- 与 dashboard 聚合、签到设置、用量统计、导出进度等模块联动。
- 导航中需要和官方 `/admin/ops` 区分：`ops` 更偏系统运维，`operations` 更偏业务运营。

关键代码：

- `backend/internal/server/routes/admin.go`
- `backend/internal/handler/admin/setting_handler.go`
- `backend/internal/handler/admin/setting_handler_operations_export_test.go`
- `backend/internal/repository/dashboard_aggregation_repo.go`
- `frontend/src/api/admin/operations.ts`
- `frontend/src/views/admin/OperationsCenterView.vue`
- `frontend/src/views/admin/operations/DailyCheckinTrendChart.vue`
- `frontend/src/views/admin/operations/OperationsOverviewTrendChart.vue`
- `frontend/src/router/index.ts`

相关提交线索：

- `af787aad4 feat: 增加签到运营中心`
- `ee04cd744 feat: 增强运营中心分析与导出`
- `6f1b04407 fix: 修复运营中心记录筛选分页`
- `912ab8a68 fix: 修复运营中心签到配置保存问题`
- `bc92bd30a fix: 优化运营中心与生图导航展示`

同步上游注意：

- 上游若调整 admin route、菜单权限、settings handler 或 dashboard repository，需确认 `/admin/operations` 入口仍可见且权限正确。
- 避免把运营中心和官方 Ops Monitoring 的接口、i18n key、菜单 key 混用。

### 3. 成本核算

定位：在官方 token 计费基础上，fork 增加人民币实际成本、账号成本和每美元成本统计，用于经营分析。

主要差异：

- 账号上增加 `total_cost_cny`，支持创建、更新、批量更新和增量成本录入。
- usage/dashboard 聚合返回 `total_account_cost`、`total_cost_cny`、`average_cost_cny_per_usd`、`today_real_cost_cny` 等字段。
- dashboard 的人民币成本卡使用账号维度小时聚合，并仅对尚未进入连续覆盖区间的明细前缀/尾部读取 `usage_logs`，避免首屏反复全表分组；历史账号成本使用独立水位按天分块、按时间预算连续回填，逐块释放常规聚合锁并输出覆盖进度，不重跑其他 dashboard 聚合维度，也不饿死实时指标。
- 管理后台 dashboard 展示人民币总成本、今日实际人民币成本、平台维度每美元人民币成本。
- 支付、订阅、订单金额显示统一为人民币口径，订阅升级抵扣和手续费/限额也有 fork 修正。

关键代码：

- `backend/ent/schema/account.go`
- `backend/internal/service/admin_service.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/repository/usage_log_repo.go`
- `backend/internal/repository/dashboard_aggregation_repo.go`
- `frontend/src/views/admin/DashboardView.vue`
- `frontend/src/types/index.ts`
- `frontend/src/components/account/AccountUsageCell.vue`
- `frontend/src/components/account/AccountTodayStatsCell.vue`

关键迁移：

- `backend/migrations/140_add_account_total_cost_cny.sql`
- `backend/migrations/141_dashboard_account_cost_aggregation_tables.sql`
- `backend/migrations/142_remove_dashboard_cost_trend.sql`
- `backend/migrations/150_restore_dashboard_account_cost_columns.sql`
- `backend/migrations/152_usage_dashboard_user_stats.sql`
- `backend/migrations/174_dashboard_account_cost_hourly.sql`

相关提交线索：

- `d5d3dc3cc 功能：新增账号人民币成本统计`
- `17d1362aa 修复：完善仪表盘每美元成本统计`
- `309071396 fix: 统一仪表盘与账号每刀成本口径`
- `02a8f8097 feat: 增加今日人民币实际成本展示`
- `87df54f17 feat: 完善支付手续费与限额口径`
- `cbfd3c5d5 fix: 收口订阅下单金额口径`

同步上游注意：

- 上游若重构 usage log、dashboard aggregation 或 account schema，必须保留 `total_cost_cny`、账号成本小时聚合，以及“聚合覆盖区间 + 原始尾部”的不重不漏逻辑。
- 成本口径涉及经营数据，合并后至少跑 dashboard/usage/account 相关测试，并人工检查后台金额单位是否仍为 `¥`。

### 4. 账号归档

定位：账号生命周期扩展。归档账号保留历史数据，但默认隐藏并停止调度/后台批量操作。

主要差异：

- 账号 schema 新增 `archived_at`，归档独立于原始 `status`，避免覆盖真实上游状态。
- 默认账号列表、调度候选、后台批量操作会过滤归档账号。
- 支持单账号归档/取消归档和批量归档，前端显示独立归档状态。
- 运维统计排除归档账号，避免历史账号污染当前容量和健康度。

关键代码：

- `backend/ent/schema/account.go`
- `backend/internal/repository/account_repo.go`
- `backend/internal/service/admin_service.go`
- `frontend/src/views/admin/AccountsView.vue`
- `frontend/src/components/account/AccountStatusIndicator.vue`
- `frontend/src/components/admin/account/AccountActionMenu.vue`
- `frontend/src/components/admin/account/AccountBulkActionsBar.vue`

关键迁移：

- `backend/migrations/148_add_account_archived_at.sql`

相关提交线索：

- `639a3e2f1 后端支持账号归档生命周期`
- `cccffd6c7 后端跳过归档账号后台操作`
- `836838853 前端支持账号归档管理`
- `d5530a071 修复归档导出与后台契约覆盖`
- `fbab8dbb7 fix: 排除归档账号的运维统计`

同步上游注意：

- 上游若改账号调度、账号列表筛选、批量编辑、导入导出、账号状态展示，需要逐处确认 `archived_at IS NULL` 过滤没有丢失。
- 不要把归档实现成普通 `status=archived`，否则会破坏原始状态和恢复后的调度判断。

### 5. Web 创作台

定位：用户侧浏览器内 OpenAI-compatible 对话与生图创作入口。

主要差异：

- 新增 Web Console 开关和默认端点设置：`web_console_enabled`、`web_console_default_endpoint`。
- 用户侧前端 `/console` 支持 OpenAI-compatible `/v1` 对话、Responses 工具调用、生图模式、本地会话存储；后端异步任务 API 使用 `/web-console/image-tasks`。
- 生图任务改为后端异步任务接口，支持任务轮询、资产恢复、删除会话时同步清理后端恢复态。
- 图片引用、蒙版和生成结果通过浏览器 Cache Storage 和后端归档互相恢复，降低刷新后丢图概率。

关键代码：

- `frontend/src/views/user/WebConsoleView.vue`
- `frontend/src/features/web-console/openaiClient.ts`
- `frontend/src/features/web-console/storage.ts`
- `frontend/src/api/webConsoleImageTasks.ts`
- `backend/internal/handler/web_console_image_task_handler.go`
- `backend/internal/server/routes/user.go`
- `frontend/src/router/index.ts`
- `frontend/src/utils/featureFlags.ts`

关键迁移：

- `backend/migrations/159_image_generation_archive.sql`
- `backend/migrations/160_web_console_image_task_user_delete.sql`

相关提交线索：

- `cab313bb1 feat: 增加网页工作台系统设置`
- `70c6469b3 feat: 接入网页工作台 OpenAI 对话`
- `e2aa0554a feat: 优化网页工作台 Responses 工具调用`
- `6a3144067 feat: 支持 Web Console 生图编辑任务`
- `5e51eada3 fix: 优化网页工作台图片生成体验`
- `619f725f5 fix: 收口网页工作台生图任务状态`
- `bb19e5fd8 fix: 恢复创作台编辑生图缓存素材`

同步上游注意：

- 上游若更新 OpenAI Responses、tool call、images 或网关错误格式，要同步检查 `openaiClient.ts` 与 `web_console_image_task_handler.go` 的请求/响应兼容。
- 上游若调整公共设置加载或路由守卫，要确认 Web Console 开关仍在刷新后生效。

### 6. 生图管理

定位：admin 侧集中查看和清理 Web 创作台生图归档资产。

主要差异：

- 新增生图归档服务和 repository，记录请求、输出资产、状态、存储统计。
- 管理后台 `/admin/image-generations` 支持状态筛选、分页加载、资产预览、启用/关闭归档、清空终态归档。
- 清空终态归档会保留等待中或归档中的记录；对象存储清理失败时保留记录，便于重试。
- 对资产访问做鉴权和安全返回，修复 CSP、签名 URL 失效、本地缓存丢失后的恢复路径。

关键代码：

- `backend/internal/service/image_generation_archive.go`
- `backend/internal/repository/image_generation_archive_repo.go`
- `backend/internal/handler/admin/image_generation_handler.go`
- `backend/internal/handler/web_console_image_task_handler.go`
- `backend/internal/server/routes/admin.go`
- `frontend/src/views/admin/ImageGenerationsView.vue`
- `frontend/src/api/admin/imageGenerations.ts`

关键迁移：

- `backend/migrations/159_image_generation_archive.sql`
- `backend/migrations/160_web_console_image_task_user_delete.sql`

相关提交线索：

- `702056526 feat: 增加生图归档与异步任务`
- `2fb77e392 fix: 补齐生图归档禁用开关`
- `4bed2d614 fix: 补齐生图归档存储统计`
- `5820ec76d fix: 改进生图归档管理体验`
- `7af485fd1 perf: 优化生图归档图片加载`
- `245ac7045 fix: 收口生图归档清空并发边界`
- `756bcde07 fix: 明确清空生图终态归档`
- `a72fefe17 fix: 收口归档图片资产安全返回`

同步上游注意：

- 上游若引入自己的图片任务、对象存储、文件代理或 CSP 处理，要检查是否和 fork 的归档表、资产签名、清理策略重复。
- 生图管理和 Web 创作台共享归档链路；debug 时不要只查前端缓存，也要查 `web_console_image_tasks` 和归档资产记录。

## 其他 fork 差异

### 订阅与支付增强

差异包括订阅升级抵扣、退款预览、支付手续费与限额、订阅绑定 API Key、周配额批量重置、高峰倍率、支付金额展示口径修正。

关键代码：

- `backend/internal/service/payment_subscription_upgrade.go`
- `backend/internal/service/payment_fulfillment.go`
- `backend/internal/service/subscription_service.go`
- `backend/internal/handler/admin/subscription_handler.go`
- `frontend/src/views/admin/orders/AdminPaymentPlansView.vue`
- `frontend/src/components/payment/SubscriptionPlanCard.vue`
- `docs/PAYMENT_CN.md`

相关提交线索：

- `68ce438bc feat(payment): support subscription upgrade credits and refund previews`
- `d9baa5011 feat(payment): show subscription upgrade credits in admin orders`
- `44fef5b28 订阅：完善支付履约、密钥绑定与签到后端`
- `0b4e6d73f feat: 支持批量重置订阅周配额`
- `f75aa0ed9 fix: 支持所有订阅类型高峰倍率`

`v0.1.150` 同步说明：

- 已采用上游支付履约 lease 和 stale worker 保护，同时保留 fork 的独立订阅记录、升级抵扣、`fulfilled_subscription_id` 和订单备注恢复逻辑。
- `SUBSCRIPTION_ASSIGNED`、`SUBSCRIPTION_SUCCESS`、已记录订阅 ID 和精确订单备注均可作为恢复锚点；恢复时仍执行幂等返佣，避免重复发放权益或漏返佣。
- 独立订阅创建、升级来源撤销、订单关联和履约审计已收敛到同一事务，并以 lease version 条件更新订单；旧 worker 失去 lease 时会整体回滚，升级目标创建失败也不会提前撤销原订阅。
- 升级订单通过 `upgrade_claim_active` 和部分唯一索引独占来源订阅；取消、过期及未支付的渠道创建失败会释放占用，历史未占用的已支付订单会在获取履约 lease 时原子补领，避免同一剩余价值被并发重复抵扣。
- 来源订阅软删除现在必须实际影响一行；对于旧版本已撤销来源并写入 `SUBSCRIPTION_UPGRADE_CREDIT_APPLIED`、但尚未创建目标订阅的半完成订单，重试会校验审计中的来源 ID 并继续原子发放。没有匹配审计的已删除来源仍拒绝自动恢复，避免误用其他订单或管理员撤销的权益。
- 订阅配额重置已接入上游原子 `ResetUsageWindows` 与窗口起点 CAS 参数；fork 的周配额批量重置继续保留，批量重置按单订阅原子提交所选窗口。

### OpenAI 路由与调度增强

差异包括严格优先级调度、试验性调度、路由解释、上游余额展示、Responses 原生工具账号能力区分、Codex 官方额度重置、工具调用参数归一化。

关键代码：

- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/handler/openai_gateway_handler.go`
- `backend/internal/pkg/openai/`
- `frontend/src/components/account/OpenAIQuotaResetCell.vue`

相关提交线索：

- `d55113a74 feat: 增加 OpenAI 严格优先级调度策略`
- `eca091c08 feat: 新增 OpenAI 试验性调度`
- `53543364d feat: 增加 OpenAI 路由解释和上游余额`
- `2767be90b fix: 区分 Responses 原生工具账号能力`
- `582399afb feat: 增加 Codex 官方额度重置`
- `a8b927ab3 fix: 兼容 Codex 工具调用参数类型`

`v0.1.150` 同步说明：

- 已迁入 compact SSE 原始 output item 保留、缺失 compaction item 补全、SSE 心跳提交后的协议错误回传，以及 API Key compact 强制 JSON `Accept`。
- 已迁入 GPT-5.6 `max` reasoning effort、候选模型后缀推导、Codex compact `max -> xhigh` 降级和 Codex `0.144.1` User-Agent 一致性。
- OpenAI 缓存读写 token 现按多种上游字段解析，普通输入、cache read、cache write 三类计费桶保持互斥，避免 cache write 重复计费。
- Anthropic 转 Responses/Chat Completions 时会同时保留总输入、cache read 和 cache creation 明细；非流式及流式终止事件不再把缓存创建 token 隐藏为普通输入，避免下游成本核算口径漂移。

`v0.1.151` 合入后的 fork 链路加固：

- 渠道缓存首次加载失败时显式 fail-closed；刷新失败时仅短暂沿用最近一次成功快照，不再把数据库故障伪装成“无渠道限制”。
- OpenAI 请求在账号选定后，按渠道映射、账号映射和 compact 映射得到的最终模型预检价格；图片模型交由媒体计费链路。上游完成后若仍缺价，会持久化 `pricing_pending` 原始用量供对账，不再生成零费用已结算账单。所有计费任务在 worker 队列拒绝或停止时同步兜底，`drop/sample` 配置不再影响账务完整性。
- Responses 显式图片请求继续在路由前抢占图片并发槽；Codex bridge 自动注入的图片工具则按最终账号归一化 payload 抢槽。WebSocket 每个 turn 使用同一最终 payload 语义，并在 turn 结束或上游早期 failover 时释放槽位。
- Responses 流读取超时、单行超限和异常 EOF 不再由 service 注入非终态 `type:error`；由 handler 统一发出一个规范的 `response.failed` 终态。
- Responses→Chat 缓冲转换兼容上游将 `web_search_call.action` 返回为字符串，避免整条终态事件因非关键字段反序列化失败而被丢弃。
- Codex models manifest 只从组内选择可解析出 backend access token 的 OpenAI OAuth/影子账号；纯 API Key 组返回合法空 manifest，让客户端保留随版本发布的内置模型 catalog，避免 `/v1/models?client_version=...` 持续返回 502/503，也避免不完整元数据覆盖 Codex 内置指令。
- Chat Completions 与 Responses 恢复 `model_not_found` / 临时容量不足分类；compact 与 Responses 原生能力不足仍保留专用错误语义。
- 图片归档把启用检查、记录创建、解码和存储统一纳入有界后台任务，并设置总超时；超时后使用独立 cleanup context 持久化失败终态，避免请求完成后无界保活 base64 数据或记录永久停在 pending/running。
- 上游异步图片任务入口使用 `image_storage.max_inflight_tasks` 和 `image_storage.max_inflight_tasks_per_api_key` 做进程内有界准入；恢复扫描不能只依赖 `pending_object_keys`，必须同时覆盖上传前崩溃的 `processing` 任务。
- 非流式响应体与 SSE 单行默认上限统一为 `128MB`，部署样例和中文文档使用同一口径，兼顾 4K base64 图片与内存保护。

### 通知、风控与内容审计增强

差异包括渠道监控全局通知、Bark 通知模板、通知免打扰、风控 hash 白名单、内容审计用户处置、邮件群发和退订。

关键代码：

- `backend/internal/service/notification_service.go`
- `backend/internal/service/notification_email_service.go`
- `backend/internal/service/content_moderation.go`
- `backend/internal/repository/content_moderation_hash_cache.go`
- `frontend/src/views/admin/RiskControlView.vue`
- `frontend/src/views/admin/EmailBroadcastsView.vue`
- `frontend/src/views/public/EmailUnsubscribeView.vue`

相关提交线索：

- `3cfd0a354 feat: 增加渠道监控全局通知设置`
- `d14c668ef feat: 增加通知免打扰时段配置`
- `4d6eda7fb feat: 支持 Bark 通知模板`
- `7f03cef37 feat: 支持风控输入 Hash 加白`
- `a8e4d6133 feat: 持久化风控 Hash 白名单`
- `0c53c55ee feat: 增加内容审计用户处置策略`
- `e001ba6c9 feat: 完善邮件群发和退订管理`

### 批量生图 MVP

当前仓库还有批量生图相关文档和实现，和 Web 创作台/生图管理的归档链路有关，但它是另一条批处理任务线，不应和 Web Console 生图任务混淆。

关键文档和代码：

- `docs/BATCH_IMAGE_MVP.md`
- `frontend/src/views/user/BatchImageGuideView.vue`
- `backend/internal/service/batch_image_public.go`
- `backend/internal/service/batch_image_billing_recovery.go`
- `backend/migrations/159_batch_image_foundation.sql` 及后续 `batch_image` 迁移

### 发布与 bcol.cc 部署

当前 fork 的发布链路按实际生产目标 `root@bcol.cc` 收敛，只发布 `linux/amd64`：

- GitHub Release 保留 `sub2api_<version>_linux_amd64.tar.gz` 与 `checksums.txt`，用于在线更新和 GHCR 拉取失败时的手动兜底。
- Docker 镜像只推送 GHCR 单架构标签：`ghcr.io/doodlexu/sub2api:<version>`、`:<version>-amd64`、`:latest`。
- 不再发布 Windows / Darwin / Linux arm64 二进制，不再构建 arm64 镜像、DockerHub 镜像或 multi-arch manifest。
- `CI` 和 `Security Scan` 只在分支 push / PR / schedule 跑，tag push 只触发 `Release`，避免同一发版 SHA 重复跑检查。
- 发版 tag 必须指向已推送到默认分支且 `CI` / `Security Scan` 已通过的 commit；不要对未经过分支检查的临时 SHA 直接打 tag。

同步上游注意：

- 上游如恢复多架构 GoReleaser、DockerHub 或 QEMU 配置，除非生产目标变化，否则不要重新引入。
- bcol.cc 是 Docker Compose 部署，更新时优先使用 `ghcr.io/doodlexu/sub2api:<version>`，必要时可用 linux/amd64 release asset 在服务器上重建本地镜像。

## 待关注上游 main 变更

当前 fork 已包含官方 release `v0.1.162`。上游 `main` 在该 release 后的提交尚未进入当前 fork；后续同步时仍应重新读取 GitHub Releases 元数据，并重点复核入口鉴权安全、Prompt Audit、Agent Identity、Grok OAuth/media 路由、Responses/WS 协议、异步图片任务与账号归档过滤，不能沿用旧 release 的提交清单推断最新状态。

## v0.1.162 合并验证

- 冲突处理：共 31 个冲突路径，含 4 个 modify/delete；设置 handler/service 与 OpenAI response handling 等上游拆分语义已移植到 fork 聚合模块，未恢复已删除文件。
- 后端：重新生成 Wire 依赖注入代码；`go test ./...` 全量通过，重点覆盖入口 IP 设置、动态图片存储、异步任务、OpenAI/Codex/Grok、运营报告和订阅剩余天数。
- 前端：Vitest 全量 `194` 个测试文件、`1369` 项用例通过；`vue-tsc --noEmit`、ESLint 和生产构建通过。账号工具菜单新增自动倍率探测翻译覆盖，长标签布局保持控件可见。
- 安装与发布：pnpm 11 frozen install 通过，安全 overrides 与 lockfile 对齐；`backend/cmd/server/VERSION` 更新为 `0.1.226`，部署和在线更新仓库继续指向 fork。
- 保留性：签到、运营中心、人民币成本、账号归档、Web 创作台、生图管理、Ops 动态直方图和生图 TTFT 入口均保留；转发 IP 信任继续默认关闭。
- 全面审核修复后验证：后端 `TZ=UTC go test -tags=unit ./... -count=1` 与 `TZ=UTC go vet -tags=unit ./...` 全量通过；动态图片存储快照、故障恢复及 Grok 协议边界专项通过 `-race`。前端 Vitest `195` 个测试文件、`1373` 项用例通过，`vue-tsc --noEmit`、ESLint 与生产构建通过；构建仅保留既有动态导入和 chunk-size 提示。

## v0.1.159 + v0.1.160 + v0.1.161 合并验证

- 冲突处理：共 53 个冲突路径，其中 43 个内容冲突、10 个 modify/delete；上游拆分文件行为继续移植到 fork 聚合模块，未恢复已删除的拆分文件。
- 后端专项：API Key 入口保护、Prompt Audit 路由覆盖、Grok media、过期订阅续期、运营设置快照、Responses 流读取错误和 WS passthrough 生命周期用例通过。
- 前端专项：运行时中英文键集合完全对称，截图涉及的 `admin.accounts.columns.upstreamBillingRate` 与 `admin.accounts.upstreamBilling.notProbed` 可正确翻译；账号主页安全链接、上游倍率提示/排序和 Prompt Audit 模块化语言包回归通过。
- 部署与保留性：Docker 跨架构构建缓存和 Redis 持久化参数修复已合入；GHCR/更新仓库仍指向 fork；签到、运营中心、人民币成本、账号归档、Web 创作台和生图管理关键入口仍存在。
- 发布版本：`backend/cmd/server/VERSION` 更新为 `0.1.225`。
- 全量验证：后端 `TZ=UTC go test -tags=unit ./... -count=1`、`TZ=UTC go vet -tags=unit ./...` 全量通过，并对 Prompt Audit 配置重载、异步图片恢复及有界准入执行定向 `-race`；`golangci-lint v2.9.0` 为 `0 issues`。前端使用 pnpm 9 frozen install 后，Vitest 共 `192` 个测试文件、`1356` 项用例通过，`vue-tsc --noEmit`、ESLint 与生产构建通过；生产依赖审计例外校验通过。Docker Compose 使用校验占位环境变量完成配置解析。

## v0.1.157 + v0.1.158 合并验证

- 冲突预检：`git merge-tree` 确认 68 个冲突，其中 50 个内容冲突、18 个 modify/delete；按 fork 差异台账逐项移植上游拆分文件语义。
- 后端：补齐聚合文件覆盖上游拆分文件后遗漏的异步生图任务鉴权、倍率自省、审计/step-up 路由、Grok OAuth 凭证换号、Responses/WS failover、首帧超时、图片 JSON keepalive、上游成本调度设置等语义；`TZ=UTC go test -tags=unit ./... -count=1` 全量通过。
- 前端：`vue-tsc --noEmit` 通过；账号复制、上游计费探测、审计日志入口、i18n 编译和高级调度 fork 文案的 46 项定向测试通过；全量 Vitest 共 `182` 个测试文件、`1299` 项用例通过，生产构建通过。
- 保留性检查：签到、运营中心、人民币成本、账号归档、Web 创作台、生图管理及 Ops 生图 Avg 关键入口仍存在；账号页同时展示上游复制/计费探测与 fork 归档能力。
- 合并后全面审核修复：恢复用户 TOTP step-up 路由及账号/代理/备份/运营导出的前端挑战链，下载类 Blob 错误会先还原结构化 step-up code；并发敏感操作复用同一个进行中 step-up Promise，避免 resolver 覆盖导致请求永久挂起。fork `/admin/operations/export` 同步纳入 step-up 与敏感读取审计。无 `sid` 旧 JWT 按 token 指纹隔离授权；密码、密码 + TOTP、OAuth、OAuth + TOTP 登录审计均补齐 actor 和真实认证方式，OAuth 绑定的 TOTP challenge 在密码确认后即记录实际用户与 `password`，LinuxDo 新用户快捷 callback 只在成功确认 actor 后记 OAuth 审计且整段省略 `code/state` 查询串。运行时中英文单体语言包同步提供筛选文案；账号复制幂等键按管理员隔离；普通与 Spark 影子账号均展示实际 OpenAI `auth_mode`。审计清空使用 advisory 写屏障，并在取锁后以数据库时钟生成水位，将 `TRUNCATE + 留痕` 放在同一事务；只有事务内同步留痕的 `extra.clear_watermark=true` 可推进水位，失败 TOTP/失败清空请求不会误删排队日志。
- OpenAI/WS 审核修复：Agent Identity 及其 Spark 影子账号的 ctx-pool、WS v2 direct、WS v2 passthrough 均支持空 bearer、逐次握手 assertion 刷新和 invalid-task 单次自愈，shadow 恢复先解析母账号再更新 task；Responses Lite WS 恢复工具载体规范化，Grok 图片意图恢复平台感知；HTTP bridge 与 WS 直连统一对 dial/error/failed/incomplete 终态执行瞬时冷却并把真实终态写入 usage 结果。Agent Identity Responses、Chat、Messages、`count_tokens`、HTTP passthrough、SSE 和 WS v2 错误在解析、Ops 记录及下游转发前完成凭证脱敏；shadow 母账号解析失败时 fail-closed，旧非流式 helper 也只返回通用安全错误体。流式 shadow 每请求只解析一次母账号，task 恢复后刷新请求级凭证快照；WS close/read/relay 错误和 `count_tokens` transport/read 错误均先按原始错误分类，再使用安全副本写日志、Ops、hooks 和下游响应。发布 lint 复核还按 v0.1.158 原位恢复了 Messages 路径遗漏的 Grok Free function-tool cache route，并删除已由 v158 拆分文件接管的旧 passthrough/streaming dead code。
- 异步图片与运行时审核修复：上游图片 URL 仅允许 HTTPS，下载前和每次重定向校验公网地址，默认 transport 在建连前固定已校验 IP 以阻断 DNS rebinding；允许通用二进制 MIME，但最终必须通过真实图片魔数校验。任务完成和失败写回使用独立有界 context 与 watchdog，Redis 以原子 `processing -> completed|failed` CAS 保证终态不可逆；每次完成尝试使用唯一对象 key，并记录已上传 key。多图部分失败、CAS 前置失败和明确 CAS 落败会补偿删除；Transition 返回错误属于提交结果未知，使用独立 5 秒预算重读终态：同一 candidate 已完成则保留并视为成功，其他终态才清理，仍为 processing 或无法确认时保留对象交由生命周期清扫，避免把已提交完成任务引用的对象误删。内容审核 worker 改复用运行时快照，session binding 设置增加 singleflight + TTL 缓存并在设置更新时立即刷新，避免每个 JWT 请求和每个 worker 空轮询都访问数据库。
- 全面审核修复后验证：后端全量 `TZ=UTC go test -tags=unit ./... -count=1` 通过，`golangci-lint v2.9.0` 为 `0 issues`；前端 Vitest `183` 个测试文件、`1304` 项用例全量通过，ESLint、`vue-tsc` 与生产构建通过；三组子代理复核及主代理交叉审查提出的管理面、OpenAI 网关、异步图片和前端/fork 保留性问题均已逐项修复并补回归覆盖。
- 再次深审修复：Agent Identity 请求统一绑定母账号、实际发送的 task ID 与请求级 redactor，shadow 并发恢复只对失败 assertion 的 task 做 CAS，流式 terminal envelope 及 scanner/read 错误均先脱敏；Grok 原生 cache 工具注入从“所有 OAuth”收紧为已确认 Free 账号，付费、未知和 API Key 请求只写 cache identity。异步生图任务在 Redis 私有记录中持久化待确认对象 key，失败终态 CAS 获胜后删除并清空清单，`PutObject` 结果不明确和部分多图失败也补偿删除唯一尝试 key；session binding 缓存以 generation 阻止 inflight 旧查询覆盖管理员新设置。备份页按统一顶层 `status` 识别 409，台账同步更正 Web Console 前端 `/console` 与后端 `/web-console/image-tasks` 的路由边界。
- 深审补强修复：Agent Identity 的安全错误副本不再保留可 `Unwrap`/`errors.As` 取回的原始敏感 cause，scanner/WS 等路径先使用原始错误完成分类，再仅向日志、hooks 与下游传播脱敏文本；请求级 redactor 会动态绑定实际 Authorization 中发送的 task ID，并通过同一请求上下文复用于 HTTP/SSE 全生命周期。异步生图会先下载并生成完整唯一 key 清单、先以 Redis CAS 持久化待清理对象清单、再执行首个 S3 `PutObject`；不明确的 Put 延迟重复删除，失败任务轮询改为按 task 去重的后台有界清理，S3 故障不再把 GET 阻塞至 15 秒。session binding 的 generation 检查、读取方 cache store 与管理员刷新发布使用同一互斥锁，封闭 generation 增长与新值写入之间被旧查询抢占的窗口。
- 深审补强验证：敏感 error 的 `Unwrap`/`errors.As` 隔离、on-wire task ID 脱敏、图片上传前清单持久化、轮询后台清理和 session binding 并发测试通过，并对新增并发用例执行 `-race`；最终态后端全量 `TZ=UTC go test -tags=unit ./... -count=1` 通过，`golangci-lint v2.9.0` 为 `0 issues`。前端 Vitest `183` 个测试文件、`1305` 项用例、ESLint、`vue-tsc --noEmit` 和生产构建继续通过，仅保留既有构建提示。
- 再次深审修复：Agent Identity 锁内重读以数据库最新认证模式为准，重读失败或认证模式已变化时禁止旧凭据快照回写；OpenAI `http_bridge` 对 `invalid_task_id` 在输出客户端错误前执行一次 task 恢复、刷新 assertion 并重试。异步图片存储的启动生命周期探针限制为 15 秒，且仅接受真正覆盖生成 key 的无过滤或前缀规则，Tag/Size/And 规则不再被误判为全量覆盖。
- 再次深审修复后验证：`TZ=UTC go test -tags=unit ./... -count=1` 全量通过，`golangci-lint v2.9.0` 为 `0 issues`；前端 Vitest `183` 个测试文件、`1305` 项用例通过，ESLint、`vue-tsc --noEmit` 与生产构建通过。构建仅保留既有的动态导入和 chunk-size 提示。

## v0.1.153 合并验证

- 后端：修复支付 handler 测试调用与上游构造函数签名漂移后，`TZ=UTC go test -tags=unit -count=1 ./...` 全量通过。
- OpenAI/Grok：模型映射、Grok Chat/Responses bridge、Responses Lite 工具兼容专项测试通过；保留 fork 原生 Responses 路由与严格优先级诊断。
- 前端：`vue-tsc --noEmit`、账号状态/密钥使用/价格配置专项 Vitest 和生产构建通过。
- 部署：保留 `.env` 权限加固，不引入上游仅适配 Apple Container、`linux/arm64` 和官方镜像的部署入口；fork 继续维持 `linux/amd64` 发布口径。
- 合并后审核修复：将 v0.1.153 新增文案补入 fork 实际加载的中英文单体语言包；恢复站点 Logo、文档 URL 安全过滤和侧栏滚动位置持久化；补齐 Gemini 批量生图配置、平台动态价格提示与对应回归测试。
- Grok 错误处理：`/v1/messages` 上游错误统一经过 Grok 专用限流策略，并在 failover 错误中保留响应头；新增 429 `Retry-After` 回归测试。
- 二次审核修复：`/v1/alpha/search` 接入 fork 内容审计和 cyber 会话屏蔽，审计输入同时覆盖 Responses `input` 与 `commands.search_query[].q`；恢复 pool-mode 的同账号重试次数与取消语义，并补充 handler 级阻断、重试回归测试。
- 三次审核修复：Alpha Search 上游 `cyber_policy` 现复用 fork 风控记录、通知、用量与会话屏蔽写入链，body `id` 作为该端点的显式会话键 fallback；非 failover 错误保持默认原样透传，但接入错误规则和 Ops 上游上下文，failover 尝试补记 Ops 事件并保留诊断响应头。
- 四次审核修复：cyber 会话屏蔽改为使用独立短超时、在异步落库/邮件/计费前同步写入，避免立即重试抢跑或共享 context 超时后丢失；Alpha Search 错误规则改为先于账号冷却副作用匹配，规则改写响应在 Ops 中标记为非原样透传。
- 五次审核修复：Alpha Search 非 failover 错误复用按端点上限读取的完整响应体，避免共享错误处理再次按默认 512 KiB 日志读取上限截断客户端响应；日志与 Ops 明细仍独立按配置截断。
- 六次审核修复：补充 cyber 会话屏蔽运行时设置的 deadline 回归覆盖，并同步更正 Alpha Search 的 2xx、3xx、普通错误与 failover 返回值契约注释；最终并发等待策略见下一条。
- 七次审核修复：cyber 会话屏蔽运行时设置的 singleflight 等待改为 `DoChan` + 调用方 context 选择，首调用者和重复调用者均按自身 deadline 返回；共享刷新继续使用独立 5 秒上限，短调用不会中止刷新或污染缓存，并补充两类并发时序回归测试。同步安全门未写入时在 30 秒审计链内使用独立 7 秒预算做一次幂等补写，避免超时路径永久丢失屏蔽且不耗尽后续审计任务时间。
- 发版前 gate 修复：Alpha Search Ops 事件测试改用显式 `Get + comma-ok` 类型断言；恢复 fork 在 `v0.1.221` 前已执行的 Grok 清理策略，移除上游合并重新带回但未接入真实路由的 `forwardGrokResponses` 草稿及其专用测试，避免以 lint 豁免掩盖死代码。
- 修复后验证：前端 Vitest `161` 个测试文件、`1088` 项用例全量通过，`vue-tsc --noEmit`、ESLint 与生产构建通过；后端 `TZ=UTC go test -tags=unit -count=1 ./...` 全量通过。

## v0.1.150 合并验证

- 后端：支付 lease/恢复、订阅配额、OpenAI reasoning/compact/cache 专项测试通过；修复 handler 测试 stub 接口漂移后，`TZ=UTC go test -tags=unit ./...` 全量通过。
- 合并后审核修复：新增 stale lease 独立订阅事务回滚、升级目标创建失败回滚来源撤销、批量配额原子失败不部分重置测试。
- 二次全面审核修复：新增升级来源唯一占用与取消释放、历史半完成升级恢复、重复占用已支付订单失败归档、重复软删除拒绝，以及 Anthropic -> Responses/Chat cache creation 明细透传测试。
- 前端：`vue-tsc --noEmit`、路由 `feature-access.spec.ts` 和生产构建通过。
- 路由守卫仅在公共设置明确返回 `false` 时禁用 payment/risk control，Web 创作台仍由 `web_console_enabled` 控制。

## 后续维护流程

每次同步上游或做大功能迭代时，按下面顺序维护本文：

1. 更新上游 release/main 引用，并记录新的基线 commit。
2. 先跑无副作用对比：

```bash
git log --oneline --left-right --cherry-pick HEAD...refs/remotes/upstream/main
git diff --stat HEAD..refs/remotes/upstream/main
git diff --name-status HEAD..refs/remotes/upstream/main
git merge-tree --write-tree HEAD refs/remotes/upstream/main
```

3. 若涉及本文核心 6 项，先在对应章节补充影响点，再合并代码。
4. 合并后至少检查：

```bash
cd backend && go test -tags=unit ./...
pnpm --dir frontend exec vue-tsc --noEmit
pnpm --dir frontend run build
```

5. 对高风险模块补充专项验证：

- 签到：签到资格、预算耗尽、余额记录、运营中心导出。
- 运营中心：路由权限、筛选分页、导出字段。
- 成本核算：账号 `total_cost_cny`、dashboard 聚合、今日实际成本。
- 账号归档：默认列表过滤、调度排除、批量归档/取消归档。
- Web 创作台：对话、生图、任务轮询、刷新恢复、会话删除。
- 生图管理：资产鉴权、分页、清空终态归档、对象存储失败重试。

## 快速定位命令

```bash
# 查看 fork 自有提交
git log --oneline --right-only --cherry-pick refs/tags/upstream/v0.1.153^{}...HEAD

# 按关键词找功能提交
git log --oneline --grep='签到\|运营\|成本\|归档\|创作台\|生图' --regexp-ignore-case

# 查看与上游 release 的文件差异
git diff --name-status refs/tags/upstream/v0.1.153^{}..HEAD

# 查看某个功能的代码入口
rg -n 'daily_checkin|web_console|image_generation|archived_at|total_cost_cny|OperationsCenter'
```
