-- USD nanounits; price snapshot 2026-10-19, peak cache-miss Flash rates.
CREATE TABLE IF NOT EXISTS pilot.model_limits (
 provider_model text PRIMARY KEY,global_limit integer NOT NULL CHECK(global_limit BETWEEN 1 AND 16),
 team_limit integer NOT NULL CHECK(team_limit BETWEEN 1 AND 16));
CREATE TABLE IF NOT EXISTS pilot.model_budgets (
 experiment text PRIMARY KEY,budget_nano bigint NOT NULL CHECK(budget_nano BETWEEN 1 AND 2000000000),
 reserved_nano bigint NOT NULL DEFAULT 0 CHECK(reserved_nano>=0),calls integer NOT NULL DEFAULT 0,
 currency text NOT NULL DEFAULT 'USD',price_date date NOT NULL DEFAULT '2026-10-19');
CREATE TABLE IF NOT EXISTS pilot.model_attempts (
 attempt_id text PRIMARY KEY,run_id text NOT NULL REFERENCES day12.agent_runs(run_id),
 experiment text NOT NULL REFERENCES pilot.model_budgets(experiment),provider_model text NOT NULL,
 team text NOT NULL,reservation_nano bigint NOT NULL,charged_nano bigint,
 input_tokens bigint,output_tokens bigint,usage_known boolean NOT NULL DEFAULT false,
 outcome text NOT NULL DEFAULT 'uncertain',provider_request_id text,
 source_file text,source_commit text,source_sha256 text,input_digest text,
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),finished_at timestamptz,
 held_until timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS pilot_attempt_admission ON pilot.model_attempts(provider_model,held_until);
ALTER TABLE pilot.model_attempts ADD COLUMN IF NOT EXISTS source_file text;
ALTER TABLE pilot.model_attempts ADD COLUMN IF NOT EXISTS source_commit text;
ALTER TABLE pilot.model_attempts ADD COLUMN IF NOT EXISTS source_sha256 text;
ALTER TABLE pilot.model_attempts ADD COLUMN IF NOT EXISTS input_digest text;
