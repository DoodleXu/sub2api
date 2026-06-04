-- Daily check-in operational controls and audit linkage.

INSERT INTO settings (key, value, updated_at)
VALUES
    ('daily_checkin_enabled', 'true', NOW()),
    ('daily_checkin_required_usage_usd', '1.00000000', NOW()),
    ('daily_checkin_usage_scope', 'actual_cost', NOW()),
    ('daily_checkin_daily_budget_usd', '0.00000000', NOW()),
    ('daily_checkin_monthly_budget_usd', '0.00000000', NOW()),
    ('daily_checkin_user_monthly_limit_usd', '0.00000000', NOW())
ON CONFLICT (key) DO NOTHING;

ALTER TABLE user_checkins
    ADD COLUMN IF NOT EXISTS redeem_code_id BIGINT REFERENCES redeem_codes(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_user_checkins_created_at
    ON user_checkins (created_at);

CREATE INDEX IF NOT EXISTS idx_user_checkins_checkin_date
    ON user_checkins (checkin_date);

CREATE INDEX IF NOT EXISTS idx_user_checkins_redeem_code_id
    ON user_checkins (redeem_code_id);
