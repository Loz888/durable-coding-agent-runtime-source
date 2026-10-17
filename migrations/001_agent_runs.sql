CREATE SCHEMA IF NOT EXISTS day12;

CREATE TABLE IF NOT EXISTS day12.agent_runs (
    run_id TEXT PRIMARY KEY,

    repo TEXT NOT NULL,
    provider TEXT NOT NULL,
    goal TEXT NOT NULL,

    status TEXT NOT NULL
        CHECK (
            status IN (
                'running',
                'succeeded',
                'failed'
            )
        ),

    revision BIGINT NOT NULL DEFAULT 0
        CHECK (revision >= 0),

    turns INT NOT NULL DEFAULT 0
        CHECK (turns >= 0),

    calls INT NOT NULL DEFAULT 0
        CHECK (calls >= 0),

    final_text TEXT NOT NULL DEFAULT '',

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS day12.checkpoints (
    run_id TEXT NOT NULL
        REFERENCES day12.agent_runs(run_id)
        ON DELETE CASCADE,

    revision BIGINT NOT NULL,

    history JSONB NOT NULL
        CHECK (jsonb_typeof(history) = 'array'),

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (run_id, revision)
);

CREATE INDEX IF NOT EXISTS idx_agent_runs_status
    ON day12.agent_runs(status);
