-- A durable signal for evidence-only completion, not extra execution permission.
ALTER TABLE day12.agent_runs ADD COLUMN IF NOT EXISTS completion_pending boolean NOT NULL DEFAULT false;
