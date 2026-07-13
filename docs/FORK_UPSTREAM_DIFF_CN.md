# Fork 与上游功能差异维护台账

本文用于记录 `DoodleXu/sub2api` fork 相对上游官方仓库 `Wei-Shaw/sub2api` 的定制功能差异，方便后续同步上游、迭代和 debug。

最后更新：2026-07-13

## 当前对比基线

| 项目 | 当前值 | 说明 |
| --- | --- | --- |
| Fork 远端 | `origin = DoodleXu/sub2api` | 当前工作主线 |
| 上游远端 | `upstream = Wei-Shaw/sub2api` | 官方原版仓库 |
| Fork 同步前 HEAD | `fc1c27d06 chore: 准备发布 v0.1.221` | 本次 merge 前基线，fork 版本继续保留 `0.1.221` |
| 上游最新 release 基线 | `refs/tags/upstream/v0.1.153` -> `a2bc133747` | 本次已合入，release 页面：`https://github.com/Wei-Shaw/sub2api/releases/tag/v0.1.153` |
| 上游 main HEAD | `7d239d62e` | `v0.1.153` 发布后的 main 仍有后续提交，尚未作为完整同步进入当前 fork |
| fork 相对上游 release 差异 | fork 仍保留自定义功能差异 | 本次解决 21 个冲突文件，继续保留 fork 聚合文件结构和六类核心定制行为，同时迁入上游 Grok、Codex 工具桥、WebSocket 与网页搜索计费能力 |

更新本文时建议先刷新引用：

```bash
git fetch origin --prune
git fetch upstream refs/heads/main:refs/remotes/upstream/main --no-tags
git fetch upstream refs/tags/v0.1.153:refs/tags/upstream/v0.1.153 --force
git log --oneline --right-only --cherry-pick refs/tags/upstream/v0.1.153^{}...HEAD
git diff --name-status refs/tags/upstream/v0.1.153^{}..HEAD
```

如上游 release tag 更新，先把 `v0.1.153` 替换为新的官方 release tag，再更新本节。

本次 `v0.1.153` 合并说明：

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
- 用户侧 `/web-console` 支持 OpenAI-compatible `/v1` 对话、Responses 工具调用、生图模式、本地会话存储。
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

当前 fork 已包含官方最新 release `v0.1.153`。上游 `main` 在该 release 后的提交尚未进入当前 fork；后续同步时仍应重新读取 GitHub Releases 元数据，并重点复核 Grok OAuth/media 路由、OpenAI 计费、Responses/WS 协议、图片工具 namespace、setup-token 刷新与账号归档过滤，不能沿用旧 release 的提交清单推断最新状态。

## v0.1.153 合并验证

- 后端：修复支付 handler 测试调用与上游构造函数签名漂移后，`TZ=UTC go test -tags=unit -count=1 ./...` 全量通过。
- OpenAI/Grok：模型映射、Grok Chat/Responses bridge、Responses Lite 工具兼容专项测试通过；保留 fork 原生 Responses 路由与严格优先级诊断。
- 前端：`vue-tsc --noEmit`、账号状态/密钥使用/价格配置专项 Vitest 和生产构建通过。
- 部署：保留 `.env` 权限加固，不引入上游仅适配 Apple Container、`linux/arm64` 和官方镜像的部署入口；fork 继续维持 `linux/amd64` 发布口径。

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
