-- Optional observability context, never an execution authorization.
ALTER TABLE day12.agent_runs ADD COLUMN IF NOT EXISTS trace_parent text NOT NULL DEFAULT '';
ALTER TABLE day12.agent_runs ADD COLUMN IF NOT EXISTS trace_state text NOT NULL DEFAULT '';
