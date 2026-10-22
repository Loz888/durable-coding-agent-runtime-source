ALTER TABLE day19.repair_runs
 ADD COLUMN IF NOT EXISTS binding_generation bigint NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS artifact_sha text NOT NULL DEFAULT '',
 ADD COLUMN IF NOT EXISTS artifact_generation bigint NOT NULL DEFAULT 0,
 ADD COLUMN IF NOT EXISTS artifact_epoch bigint NOT NULL DEFAULT 0;
