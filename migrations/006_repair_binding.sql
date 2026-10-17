-- Stable managed-worktree identity for Day19 recovery; no existing rows deleted.
-- Legacy bindings with an empty identity must be explicitly audited before reuse.
BEGIN;
ALTER TABLE day19.repair_runs
    ADD COLUMN IF NOT EXISTS worktree_identity TEXT NOT NULL DEFAULT '';
-- Causal patch order must not depend on a wall clock that can move backward.
ALTER TABLE day19.patch_ops
    ADD COLUMN IF NOT EXISTS round_number INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_day19_patch_round_number
    ON day19.patch_ops(run_id,round_number) WHERE round_number > 0;
COMMIT;
