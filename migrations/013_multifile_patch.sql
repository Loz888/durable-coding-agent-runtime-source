-- One parent intent/round; the manifest is immutable and covers every member.
ALTER TABLE day19.patch_ops ADD COLUMN IF NOT EXISTS file_changes jsonb NOT NULL DEFAULT '[]'::jsonb;
