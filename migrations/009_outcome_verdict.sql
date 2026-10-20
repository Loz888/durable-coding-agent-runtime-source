-- Additive; old snapshots remain explicitly not_evaluated, never retroactively verified.
ALTER TABLE day12.agent_runs ADD COLUMN IF NOT EXISTS outcome_verdict text NOT NULL DEFAULT 'not_evaluated' CHECK(outcome_verdict IN ('not_evaluated','evidence_consistent','insufficient_evidence','inconsistent_evidence','tests_passed','tests_failed'));
ALTER TABLE day12.agent_runs ADD COLUMN IF NOT EXISTS diagnostic_report jsonb NOT NULL DEFAULT 'null'::jsonb;
