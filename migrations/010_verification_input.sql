-- Missing digest on historical verdicts requires an authorized fresh verification.
ALTER TABLE day19.patch_ops ADD COLUMN IF NOT EXISTS verification_input_digest text NOT NULL DEFAULT '';
