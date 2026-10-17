
-- Day 13: PostgreSQL-backed Agent Lease
-- 只为 Day12 的 agent_runs 增加执行所有权字段。

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    lease_owner TEXT;

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    lease_epoch BIGINT NOT NULL DEFAULT 0;

ALTER TABLE day12.agent_runs
    ADD COLUMN IF NOT EXISTS
    lease_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS
    idx_day13_agent_lease
ON day12.agent_runs(status, lease_until);
