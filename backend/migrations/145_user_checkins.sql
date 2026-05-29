-- 用户每日签到记录与奖励区间设置。

INSERT INTO settings (key, value, updated_at)
VALUES
    ('daily_checkin_reward_min_usd', '1', NOW()),
    ('daily_checkin_reward_max_usd', '3', NOW())
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS user_checkins (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    checkin_date VARCHAR(10) NOT NULL,
    reward_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    qualified_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_checkins_user_date
    ON user_checkins (user_id, checkin_date);

CREATE INDEX IF NOT EXISTS idx_user_checkins_user_month
    ON user_checkins (user_id, checkin_date);
