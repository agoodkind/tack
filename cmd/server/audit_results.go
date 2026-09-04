package main

import (
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

// auditQueryResult is the `audit query` output: one page of matching ledger
// rows, most recent first. A full page carries next_cursor; pass it back as
// --cursor for the next page.
type auditQueryResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string      `json:"command"`
	OrgID                uuid.UUID   `json:"org_id"`
	Oldest               time.Time   `json:"oldest"`
	Latest               time.Time   `json:"latest"`
	RowCount             int         `json:"row_count"`
	Rows                 []audit.Row `json:"rows"`
	NextCursor           string      `json:"next_cursor,omitempty"`
}

// auditGetResult is the `audit get` output: one ledger row.
type auditGetResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string    `json:"command"`
	Row                  audit.Row `json:"row"`
}

// auditRedactActorResult is the `audit redact-actor` output for a plan and
// for an applied run.
type auditRedactActorResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string    `json:"command"`
	DryRun               bool      `json:"dry_run"`
	OrgID                uuid.UUID `json:"org_id"`
	ActorID              uuid.UUID `json:"actor_id"`
	PIIRefCount          int       `json:"pii_ref_count"`
	Unredacted           int64     `json:"unredacted"`
	Redacted             int64     `json:"redacted"`
}

// auditExportResult mirrors audit.ExportManifest for CLI emission.
type auditExportResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	ExportID             uuid.UUID `json:"export_id"`
	OrgID                uuid.UUID `json:"org_id"`
	Oldest               time.Time `json:"oldest"`
	Latest               time.Time `json:"latest"`
	RowCount             int       `json:"row_count"`
	EventsFile           string    `json:"events_file"`
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
	ChainGapCount        int      `json:"chain_gap_count"`
	ChainBreaks          []string `json:"chain_breaks"`
	FileSHA256OK         bool     `json:"file_sha256_ok"`
	SignatureOK          bool     `json:"signature_ok"`
	ManifestSubject      string   `json:"manifest_subject"`
	ManifestSigner       string   `json:"manifest_signer"`
	SignerSetConfigured  bool     `json:"signer_set_configured"`
	SignerAllowed        bool     `json:"signer_allowed"`
}

// auditBackfillAbsorbedOrg reports one --absorb-org exemption: the rows it
// would move on a plan, and what it moved on an applied run. The top-level
// moved counters describe the nil-org move alone; each absorbed source
// carries its own, because shards of different sources overlap and a summed
// count would double-count chains.
type auditBackfillAbsorbedOrg struct {
	OrgID         uuid.UUID `json:"org_id"`
	Rows          int64     `json:"rows"`
	Shards        int       `json:"shards"`
	RowsMoved     int64     `json:"rows_moved"`
	ShardsTouched int       `json:"shards_touched"`
	Passes        int       `json:"passes"`
}

// auditBackfillOrgResult is the `audit backfill-org` output for a plan and
// for an applied run. The exemption fields name every absorbed org and
// acknowledged actor, so the report the operator files is the durable record
// of what the flags exempted.
type auditBackfillOrgResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string                     `json:"command"`
	DryRun               bool                       `json:"dry_run"`
	TargetOrg            uuid.UUID                  `json:"target_org"`
	NilRows              int64                      `json:"nil_rows"`
	Shards               int                        `json:"shards"`
	AbsorbedOrgs         []auditBackfillAbsorbedOrg `json:"absorbed_orgs,omitempty"`
	AcknowledgedActors   []uuid.UUID                `json:"acknowledged_actors,omitempty"`
	RowsMoved            int64                      `json:"rows_moved"`
	ShardsTouched        int                        `json:"shards_touched"`
	Passes               int                        `json:"passes"`
}

// auditGenKeyResult reports a generated audit signing key path.
type auditGenKeyResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string `json:"command"`
	Status               string `json:"status"`
	Path                 string `json:"path"`
}
