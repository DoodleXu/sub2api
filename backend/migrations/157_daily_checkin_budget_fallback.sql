-- Daily check-in fallback reward after daily budget exhaustion.

INSERT INTO settings (key, value, updated_at)
VALUES
    ('daily_checkin_budget_fallback_reward_usd', '0.01000000', NOW()),
    ('daily_checkin_budget_fallback_message', '今日签到预算已用完哦～奖励0.01', NOW())
ON CONFLICT (key) DO NOTHING;
