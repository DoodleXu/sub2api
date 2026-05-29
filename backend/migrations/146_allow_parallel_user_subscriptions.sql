-- 146_allow_parallel_user_subscriptions.sql
-- 允许同一用户重复购买同一订阅分组时生成多条独立订阅记录。

ALTER TABLE user_subscriptions DROP CONSTRAINT IF EXISTS user_subscriptions_user_id_group_id_key;
DROP INDEX IF EXISTS user_subscriptions_user_id_group_id_key;
DROP INDEX IF EXISTS usersubscription_user_id_group_id;
DROP INDEX IF EXISTS user_subscriptions_user_group_unique_active;

CREATE INDEX IF NOT EXISTS user_subscriptions_user_group_active_lookup
    ON user_subscriptions(user_id, group_id, status, expires_at DESC, created_at DESC)
    WHERE deleted_at IS NULL;
