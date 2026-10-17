-- Day 14: PostgreSQL-backed Agent Run Queue

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    queue_enabled BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    priority INTEGER NOT NULL DEFAULT 0;

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    retry_count INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_day14_runnable
ON day12.agent_runs (
    priority DESC,
    available_at ASC,
    created_at ASC,
    run_id ASC
)
WHERE queue_enabled = TRUE
  AND status = 'running';
