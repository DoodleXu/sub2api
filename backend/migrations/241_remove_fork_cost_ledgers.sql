-- 删除 fork 专属的人民币成本账本、脏桶、快照及其触发器。
-- 官方 usage_dashboard_daily/hourly.account_cost 统计保留，OAuth 账号的
-- accounts.total_cost_cny 手工输入保留用于账号页展示。
DROP TRIGGER IF EXISTS trg_usage_logs_account_cost_pending ON usage_logs;
DROP TRIGGER IF EXISTS trg_usage_logs_account_cost_rebuild ON usage_logs;
DROP TRIGGER IF EXISTS trg_accounts_cost_total_initialize ON accounts;

DROP FUNCTION IF EXISTS mark_account_cost_total_pending();
DROP FUNCTION IF EXISTS reset_account_cost_total_for_usage_change();
DROP FUNCTION IF EXISTS mark_account_cost_dirty_bucket(timestamptz);
DROP FUNCTION IF EXISTS apply_account_cost_delta(bigint, bigint, numeric, numeric, timestamptz);
DROP FUNCTION IF EXISTS initialize_account_cost_total();

DROP TABLE IF EXISTS usage_dashboard_cost_snapshot;
DROP TABLE IF EXISTS usage_dashboard_account_cost_daily;
DROP TABLE IF EXISTS usage_dashboard_account_cost_hourly;
DROP TABLE IF EXISTS usage_account_cost_dirty_buckets;
DROP TABLE IF EXISTS usage_account_cost_totals;
