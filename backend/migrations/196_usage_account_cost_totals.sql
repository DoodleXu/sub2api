-- 每账号累计成本账本。
--
-- 账本只记录计算结果和 usage_logs.id 检查点，不替代、不删除原始 usage_logs。
-- 账号物理删除时账本随账号级联删除；有历史的账号应优先归档，以保留审计数据。
CREATE TABLE IF NOT EXISTS usage_account_cost_totals (
    account_id BIGINT PRIMARY KEY,
    total_account_cost NUMERIC NOT NULL DEFAULT 0,
    total_standard_account_cost NUMERIC NOT NULL DEFAULT 0,
    last_processed_usage_id BIGINT NOT NULL DEFAULT 0,
    initialized BOOLEAN NOT NULL DEFAULT FALSE,
    needs_processing BOOLEAN NOT NULL DEFAULT TRUE,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $$ BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'fk_usage_account_cost_totals_account'
  ) THEN
    ALTER TABLE usage_account_cost_totals
      ADD CONSTRAINT fk_usage_account_cost_totals_account
      FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_usage_account_cost_totals_pending
    ON usage_account_cost_totals (computed_at, account_id)
    WHERE needs_processing OR NOT initialized;

-- 为所有已有账号（含历史软删除账号）建立一次性回填任务。软删除不会改变
-- usage 历史，后台仍需把这些历史成本纳入累计账本。
INSERT INTO usage_account_cost_totals (account_id)
SELECT id FROM accounts
ON CONFLICT (account_id) DO NOTHING;

-- 所有 usage 写入路径都经过该触发器，确保新增日志不会被归档状态静默漏掉。
-- PostgreSQL sequence 的 ID 分配顺序不等于事务提交顺序；若较小 ID 在检查点
-- 推进后才提交，只重建该账号，避免该条 usage 永久落在检查点之前。
CREATE OR REPLACE FUNCTION mark_account_cost_total_pending()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO usage_account_cost_totals (account_id, needs_processing)
    VALUES (NEW.account_id, TRUE)
    ON CONFLICT (account_id) DO NOTHING;

    UPDATE usage_account_cost_totals
    SET total_account_cost = CASE
            WHEN NEW.id <= last_processed_usage_id THEN 0
            ELSE total_account_cost
        END,
        total_standard_account_cost = CASE
            WHEN NEW.id <= last_processed_usage_id THEN 0
            ELSE total_standard_account_cost
        END,
        last_processed_usage_id = CASE
            WHEN NEW.id <= last_processed_usage_id THEN 0
            ELSE last_processed_usage_id
        END,
        initialized = CASE
            WHEN NEW.id <= last_processed_usage_id THEN FALSE
            ELSE initialized
        END,
        needs_processing = TRUE,
        computed_at = CASE
            WHEN NEW.id <= last_processed_usage_id THEN NOW()
            ELSE computed_at
        END
    WHERE account_id = NEW.account_id;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_usage_logs_account_cost_pending ON usage_logs;
CREATE TRIGGER trg_usage_logs_account_cost_pending
AFTER INSERT ON usage_logs
FOR EACH ROW
EXECUTE FUNCTION mark_account_cost_total_pending();

-- 显式删除 usage 或修正成本字段时，只重置受影响账号的账本。后台会按
-- 该账号重新计算，成本流程本身不会删除任何 usage。
CREATE OR REPLACE FUNCTION reset_account_cost_total_for_usage_change()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    affected_account_id BIGINT;
BEGIN
    affected_account_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.account_id ELSE NEW.account_id END;
    IF TG_OP = 'DELETE' THEN
        UPDATE usage_account_cost_totals
        SET total_account_cost = 0,
            total_standard_account_cost = 0,
            last_processed_usage_id = 0,
            initialized = FALSE,
            needs_processing = TRUE,
            computed_at = NOW()
        WHERE account_id = affected_account_id;
        RETURN OLD;
    END IF;

    INSERT INTO usage_account_cost_totals (
        account_id,
        total_account_cost,
        total_standard_account_cost,
        last_processed_usage_id,
        initialized,
        needs_processing,
        computed_at
    ) VALUES (affected_account_id, 0, 0, 0, FALSE, TRUE, NOW())
    ON CONFLICT (account_id) DO UPDATE
    SET total_account_cost = 0,
        total_standard_account_cost = 0,
        last_processed_usage_id = 0,
        initialized = FALSE,
        needs_processing = TRUE,
        computed_at = NOW();

    IF OLD.account_id IS DISTINCT FROM NEW.account_id THEN
        INSERT INTO usage_account_cost_totals (
            account_id,
            total_account_cost,
            total_standard_account_cost,
            last_processed_usage_id,
            initialized,
            needs_processing,
            computed_at
        ) VALUES (OLD.account_id, 0, 0, 0, FALSE, TRUE, NOW())
        ON CONFLICT (account_id) DO UPDATE
        SET total_account_cost = 0,
            total_standard_account_cost = 0,
            last_processed_usage_id = 0,
            initialized = FALSE,
            needs_processing = TRUE,
            computed_at = NOW();
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_usage_logs_account_cost_rebuild ON usage_logs;
CREATE TRIGGER trg_usage_logs_account_cost_rebuild
AFTER DELETE OR UPDATE OF total_cost, account_stats_cost, account_rate_multiplier, account_id ON usage_logs
FOR EACH ROW
EXECUTE FUNCTION reset_account_cost_total_for_usage_change();
