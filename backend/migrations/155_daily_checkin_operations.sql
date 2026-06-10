-- Daily check-in operation center settings and reward metadata.

INSERT INTO settings (key, value, updated_at)
VALUES
    ('daily_checkin_reward_tiers', '', NOW()),
    ('daily_checkin_streak_multiplier_enabled', 'false', NOW()),
    ('daily_checkin_streak_multiplier_scope', 'cross_month', NOW()),
    ('daily_checkin_streak_multipliers', '', NOW()),
    ('daily_checkin_crit_enabled', 'false', NOW()),
    ('daily_checkin_crit_probability_percent', '0.00000000', NOW()),
    ('daily_checkin_crit_multiplier', '1.00000000', NOW()),
    ('daily_checkin_crit_max_reward_usd', '0.00000000', NOW())
ON CONFLICT (key) DO NOTHING;

ALTER TABLE user_checkins
    ADD COLUMN IF NOT EXISTS reward_metadata JSONB;
