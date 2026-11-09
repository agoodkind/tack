package ops

import "errors"

var (
	errAuditPartSchemaVersion     = errors.New("schema_version")
	errAuditPartTicket            = errors.New("ticket")
	errAuditPartMissingIdentities = errors.New("org_id, target_event_id, and source_event_id must be set")
	errAuditPartSameEvent         = errors.New("target_event_id and source_event_id must differ")
	errAuditPartIndex             = errors.New("part_index")
	errAuditPartManifestDigest    = errors.New("manifest_sha256 must be 64 lowercase hex digits")
	errAuditPartManifestCounts    = errors.New("declared_parts must be 1 and identity_count must be positive")
	errAuditPartSequences         = errors.New("target_seq and source_seq must be greater than 0")
	errAuditPartHashVersion       = errors.New("target_hash_version must be greater than 0")
	errAuditPartRowHashes         = errors.New("target_row_hash and source_row_hash must be set")
	errAuditPartShards            = errors.New("target_shard and source_shard must be in 0..255")
	errAuditPartVerification      = errors.New("does not match the frozen manifest")
	errAuditPartReaderOpen        = errors.New(
		auditPartRestoreTool + ": the ledger reader did not open using " + auditPartReaderDSNName,
	)
	errAuditPartOperatorRequired = errors.New(
		auditPartRestoreTool + ": " + auditPartOperatorDSNName + " is required",
	)
	errAuditPartOperatorOpen = errors.New(
		auditPartRestoreTool + ": the operator pool did not open using " + auditPartOperatorDSNName,
	)
)
