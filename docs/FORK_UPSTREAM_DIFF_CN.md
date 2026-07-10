# Fork 与上游功能差异维护台账

本文用于记录 `DoodleXu/sub2api` fork 相对上游官方仓库 `Wei-Shaw/sub2api` 的定制功能差异，方便后续同步上游、迭代和 debug。

最后更新：2026-07-10

## 当前对比基线

| 项目 | 当前值 | 说明 |
| --- | --- | --- |
| Fork 远端 | `origin = DoodleXu/sub2api` | 当前工作主线 |
| 上游远端 | `upstream = Wei-Shaw/sub2api` | 官方原版仓库 |
| Fork 同步前 HEAD | `9e55df0e2 chore: 准备发布 v0.1.208` | 本次 merge 前基线，fork 版本继续保留 `0.1.208` |
| 上游最新 release 基线 | `refs/tags/upstream/v0.1.150` -> `0dec1ad292` | 本次已合入，release 页面：`https://github.com/Wei-Shaw/sub2api/releases/tag/v0.1.150` |
| 上游 main HEAD | `5260a42a0b` | 上游 main 在 `v0.1.150` 后还有 13 个提交，尚未进入当前 fork |
| fork 相对上游 release 差异 | 约 276 个提交、470 个文件 | 以本次 merge 暂存树相对 `refs/tags/upstream/v0.1.150^{}` 统计 |

更新本文时建议先刷新引用：

```bash
git fetch origin --prune
git fetch upstream refs/heads/main:refs/remotes/upstream/main --no-tags
git fetch upstream refs/tags/v0.1.150:refs/tags/upstream/v0.1.150 --force
git log --oneline --right-only --cherry-pick refs/tags/upstream/v0.1.150^{}...HEAD
git diff --name-status refs/tags/upstream/v0.1.150^{}..HEAD
```

如上游 release tag 更新，先把 `v0.1.150` 替换为新的官方 release tag，再更新本节。

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

相关提交线索：

- `d5d3dc3cc 功能：新增账号人民币成本统计`
- `17d1362aa 修复：完善仪表盘每美元成本统计`
- `309071396 fix: 统一仪表盘与账号每刀成本口径`
- `02a8f8097 feat: 增加今日人民币实际成本展示`
- `87df54f17 feat: 完善支付手续费与限额口径`
- `cbfd3c5d5 fix: 收口订阅下单金额口径`

同步上游注意：

- 上游若重构 usage log、dashboard aggregation 或 account schema，必须保留 `total_cost_cny` 和账号成本聚合兜底逻辑。
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
- 订阅配额重置已接入上游原子 `ResetUsageWindows` 与窗口起点 CAS 参数；fork 的周配额批量重置继续保留。

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

## 待关注上游 main 变更

当前 fork 已合入上游最新 release `v0.1.150`，但上游 `main` 在该 release 后还有 13 个提交，后续同步时需要重点看：

- `f2966530c feat(openai): 支持用户级 Fast/Flex 策略`
- `de28eba3c fix(openai): harden GPT-5.6 billing and usage`
- `d3a1835ed fix(image): strip Codex image_gen namespace declarations`
- `99da30819 fix: 后台自动刷新纳入 setup-token 账号`
- `0fa1eb85e fix(grok): preserve compatible reasoning effort`
- `9a2f11b4e chore: sync VERSION to 0.1.150 [skip ci]`

风险判断：

- 用户级 Fast/Flex 策略会触及 fork 的 OpenAI 严格优先级、试验性调度和路由解释，需要避免策略层重复或优先级倒置。
- GPT-5.6 billing/usage 后续加固可能继续调整缓存 token 和 usage 完整性，应与本次迁入的互斥计费桶逐项比对。
- image_gen namespace 清理会直接影响 Web 创作台生图工具声明和 Responses 工具调用兼容。
- setup-token 后台刷新会触及账号生命周期与调度；需要确认归档账号仍被排除。

## v0.1.150 合并验证

- 后端：支付 lease/恢复、订阅配额、OpenAI reasoning/compact/cache 专项测试通过；修复 handler 测试 stub 接口漂移后，`TZ=UTC go test -tags=unit ./...` 全量通过。
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
git log --oneline --right-only --cherry-pick refs/tags/upstream/v0.1.150^{}...HEAD

# 按关键词找功能提交
git log --oneline --grep='签到\|运营\|成本\|归档\|创作台\|生图' --regexp-ignore-case

# 查看与上游 release 的文件差异
git diff --name-status refs/tags/upstream/v0.1.150^{}..HEAD

# 查看某个功能的代码入口
rg -n 'daily_checkin|web_console|image_generation|archived_at|total_cost_cny|OperationsCenter'
```
