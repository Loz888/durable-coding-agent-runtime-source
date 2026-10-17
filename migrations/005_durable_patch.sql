-- Run only against the intended database.
-- Does not modify existing day12 tables.

BEGIN;

CREATE SCHEMA IF NOT EXISTS day19;

CREATE TABLE IF NOT EXISTS day19.repair_runs (
    run_id text PRIMARY KEY,

    worktree_path text NOT NULL,
    source_commit text NOT NULL,

    max_rounds integer NOT NULL
        CHECK (max_rounds BETWEEN 1 AND 5),

    rounds_used integer NOT NULL DEFAULT 0
        CHECK (rounds_used >= 0),

    created_at timestamptz NOT NULL
        DEFAULT clock_timestamp(),

    CHECK (rounds_used <= max_rounds)
);

CREATE TABLE IF NOT EXISTS day19.patch_ops (
    run_id text NOT NULL
        REFERENCES day19.repair_runs(run_id),

    patch_id text NOT NULL,
    file_path text NOT NULL,

    before_sha text NOT NULL,
    after_sha text NOT NULL,

    old_fragment text NOT NULL,
    new_fragment text NOT NULL,

    prepared_epoch bigint NOT NULL,

    state text NOT NULL DEFAULT 'prepared'
        CHECK (
            state IN (
                'prepared',
                'applied',
                'verified_pass',
                'verified_fail'
            )
        ),

    result_epoch bigint,
    result_owner text,

    diff text NOT NULL DEFAULT '',
    test_output text NOT NULL DEFAULT '',
    test_exit_code integer,

    created_at timestamptz NOT NULL
        DEFAULT clock_timestamp(),

    updated_at timestamptz NOT NULL
        DEFAULT clock_timestamp(),

    PRIMARY KEY (run_id, patch_id)
);

CREATE INDEX IF NOT EXISTS idx_day19_patch_run
    ON day19.patch_ops(run_id, created_at);

COMMIT;
