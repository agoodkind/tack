-- Add nullable subject_id and org_id columns to audit.pii and a partial hash
-- index on (org_id, subject_id) for rows with a subject. Actor PII rows keep
-- NULL in both columns. This migration does not change grants.

-- +goose Up
ALTER TABLE audit.pii ADD COLUMN IF NOT EXISTS subject_id UUID;
ALTER TABLE audit.pii ADD COLUMN IF NOT EXISTS org_id UUID;

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS pii_subject_idx
    ON audit.pii ((org_id, subject_id) HASH)
    WHERE subject_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
DROP INDEX IF EXISTS audit.pii_subject_idx;
ALTER TABLE audit.pii DROP COLUMN IF EXISTS org_id;
ALTER TABLE audit.pii DROP COLUMN IF EXISTS subject_id;
