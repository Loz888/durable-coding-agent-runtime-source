-- Legacy workers must never claim authenticated pilot jobs and execute them on the host.
ALTER TABLE day12.agent_runs ADD COLUMN IF NOT EXISTS execution_profile text NOT NULL DEFAULT 'trusted-local' CHECK(execution_profile IN ('trusted-local','internal-pilot'));
-- Additive; legacy runs remain unowned and inaccessible through the pilot API.
CREATE SCHEMA IF NOT EXISTS pilot;
REVOKE ALL ON SCHEMA pilot FROM PUBLIC;
CREATE TABLE IF NOT EXISTS pilot.tokens (
 token_hash text PRIMARY KEY CHECK (length(token_hash)=64),
 principal text NOT NULL, team text NOT NULL,
 expires_at timestamptz NOT NULL, revoked_at timestamptz);
CREATE TABLE IF NOT EXISTS pilot.teams (
 team text PRIMARY KEY, max_pending integer NOT NULL CHECK(max_pending BETWEEN 1 AND 1000),
 per_minute integer NOT NULL CHECK(per_minute BETWEEN 1 AND 1000));
CREATE TABLE IF NOT EXISTS pilot.runs (
 run_id text PRIMARY KEY REFERENCES day12.agent_runs(run_id) ON DELETE CASCADE,
 principal text NOT NULL, team text NOT NULL REFERENCES pilot.teams(team),
 repository_id text NOT NULL, source_commit text NOT NULL,
 idempotency_key text NOT NULL, request_hash text NOT NULL,
 cancel_requested boolean NOT NULL DEFAULT false,
 submitted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(team,principal,idempotency_key));
CREATE INDEX IF NOT EXISTS pilot_team_submissions ON pilot.runs(team,submitted_at);
