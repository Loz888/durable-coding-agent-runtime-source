ALTER TABLE day19.repair_runs
 ADD COLUMN IF NOT EXISTS worktree_retired_at timestamptz,
 ADD COLUMN IF NOT EXISTS retirement_identity text NOT NULL DEFAULT '';
