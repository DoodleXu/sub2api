CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_dashboard_created_user
    ON usage_logs (created_at, user_id);
