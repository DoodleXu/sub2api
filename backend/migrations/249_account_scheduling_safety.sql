-- Retire overclocking without altering any other account configuration.
UPDATE accounts SET extra = extra - 'openai_continue_scheduling_after_limit'
WHERE extra ? 'openai_continue_scheduling_after_limit';

-- Generation and pause ownership survive stale ORM edits and background refreshes.
-- The transient manual-resume marker is only supplied by explicit admin actions.
CREATE OR REPLACE FUNCTION account_scheduling_safety_guard() RETURNS trigger AS $$
DECLARE generation bigint; manual_resume boolean;
BEGIN
  NEW.extra := COALESCE(NEW.extra, '{}'::jsonb) - 'openai_continue_scheduling_after_limit';
  IF TG_OP = 'INSERT' THEN
    NEW.extra := NEW.extra - 'scheduler_balance_pause' - 'scheduler_manual_resume' - 'scheduler_generation';
    RETURN NEW;
  END IF;
  generation := COALESCE((OLD.extra->>'scheduler_generation')::bigint, 0);
  manual_resume := NEW.extra->>'scheduler_manual_resume' = 'true';
  IF COALESCE(manual_resume, false) THEN
    NEW.extra := NEW.extra - 'scheduler_balance_pause';
    IF OLD.extra ? 'scheduler_balance_pause' THEN
      NEW.notes := concat_ws(E'\n', NULLIF(NEW.notes, ''), '[调度恢复] ' || CURRENT_TIMESTAMP::text || ' 管理员手动恢复');
    END IF;
  ELSIF OLD.extra ? 'scheduler_balance_pause' THEN
    NEW.schedulable := false;
    NEW.extra := jsonb_set(NEW.extra, '{scheduler_balance_pause}', OLD.extra->'scheduler_balance_pause');
    IF position(COALESCE(OLD.extra#>>'{scheduler_balance_pause,note}', '') IN COALESCE(NEW.notes, '')) = 0 THEN
      NEW.notes := concat_ws(E'\n', NULLIF(NEW.notes, ''), OLD.extra#>>'{scheduler_balance_pause,note}');
    END IF;
  END IF;
  IF NEW.credentials IS DISTINCT FROM OLD.credentials OR NEW.schedulable IS DISTINCT FROM OLD.schedulable OR COALESCE(manual_resume, false) THEN
    generation := generation + 1;
  END IF;
  NEW.extra := NEW.extra - 'scheduler_manual_resume' - 'scheduler_generation';
  IF generation > 0 THEN
    NEW.extra := jsonb_set(NEW.extra, '{scheduler_generation}', to_jsonb(generation));
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS accounts_scheduling_safety ON accounts;
CREATE TRIGGER accounts_scheduling_safety BEFORE INSERT OR UPDATE ON accounts
FOR EACH ROW EXECUTE FUNCTION account_scheduling_safety_guard();
