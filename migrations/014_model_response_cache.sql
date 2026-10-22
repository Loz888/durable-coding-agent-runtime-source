-- Durable logical model calls. All physical attempts are retained, including unknowns.
CREATE TABLE IF NOT EXISTS day19.model_calls (
 run_id text NOT NULL REFERENCES day19.repair_runs(run_id) ON DELETE CASCADE,
 round_number integer NOT NULL CHECK(round_number BETWEEN 1 AND 5),
 attempt_number integer NOT NULL CHECK(attempt_number BETWEEN 1 AND 3),
 context_digest text NOT NULL, request_digest text NOT NULL, model_policy text NOT NULL,
 owner_id text NOT NULL, owner_epoch bigint NOT NULL,
 state text NOT NULL CHECK(state IN ('inflight','unknown','retry_authorized','completed')),
 response_json jsonb, created_at timestamptz NOT NULL DEFAULT clock_timestamp(), finished_at timestamptz,
 PRIMARY KEY(run_id,round_number,attempt_number)
);
