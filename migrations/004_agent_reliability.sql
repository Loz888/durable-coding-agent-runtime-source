
-- Day 15: Retry Budget + Terminal Failure + Observability

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    max_attempts INTEGER NOT NULL DEFAULT 3;

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    failure_kind TEXT;

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    failure_reason TEXT;

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    first_claimed_at TIMESTAMPTZ;

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    finished_at TIMESTAMPTZ;

-- 重复执行时保留已经存在的约束。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'day12.agent_runs'::regclass
          AND conname = 'agent_runs_max_attempts_check'
    ) THEN
        ALTER TABLE day12.agent_runs
            ADD CONSTRAINT agent_runs_max_attempts_check
            CHECK (max_attempts BETWEEN 1 AND 100);
    END IF;
END
$$;

-- Checkpoint 事件日志
-- 一个 Run 的一个 Revision 只记录一个 checkpoint_saved 事件。
CREATE TABLE IF NOT EXISTS day12.run_events (
    event_id BIGSERIAL PRIMARY KEY,
    run_id TEXT NOT NULL
        REFERENCES day12.agent_runs(run_id)
        ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    lease_epoch BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    turns INTEGER NOT NULL,
    calls INTEGER NOT NULL,
    status TEXT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL
        DEFAULT clock_timestamp(),

    CONSTRAINT run_events_unique_revision_type
        UNIQUE (run_id, revision, event_type)
);

CREATE INDEX IF NOT EXISTS idx_day15_events_run
ON day12.run_events(run_id, recorded_at);

CREATE INDEX IF NOT EXISTS idx_day15_exhausted
ON day12.agent_runs(lease_until)
WHERE queue_enabled = TRUE
  AND status = 'running';
