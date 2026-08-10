package main

import (
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clispec"
)

// auditExportResult mirrors audit.ExportManifest for CLI emission.
type auditExportResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	ExportID             uuid.UUID `json:"export_id"`
	OrgID                uuid.UUID `json:"org_id"`
	Oldest               time.Time `json:"oldest"`
	Latest               time.Time `json:"latest"`
	RowCount             int       `json:"row_count"`
	FileSHA256           string    `json:"file_sha256"`
	SignatureBy          string    `json:"signing_key_id"`
	Signature            string    `json:"signature"`
}

// auditVerifyResult mirrors audit.VerifyReport for CLI emission.
type auditVerifyResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	BundleDir            string   `json:"bundle_dir"`
	RowsScanned          int      `json:"rows_scanned"`
	HashMatches          int      `json:"hash_matches"`
	LinkageOnlyRows      int      `json:"linkage_only_rows"`
	ChainGapCount        int      `json:"chain_gap_count"`
	ChainBreaks          []string `json:"chain_breaks"`
	FileSHA256OK         bool     `json:"file_sha256_ok"`
	SignatureOK          bool     `json:"signature_ok"`
	ManifestSubject      string   `json:"manifest_subject"`
}

// auditGenKeyResult reports a generated audit signing key path.
type auditGenKeyResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string `json:"command"`
	Status               string `json:"status"`
	Path                 string `json:"path"`
}
