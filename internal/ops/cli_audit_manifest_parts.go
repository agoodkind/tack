package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

const (
	// auditManifestPartsCommand identifies the command in errors and logs.
	auditManifestPartsCommand = "ops audit manifest-parts"
	// auditEventPartURISegment is the URI segment before a part index.
	auditEventPartURISegment = "/part/"
)

// auditManifestPartsInput stores the flag values that the audit event
// records as safe input.
type auditManifestPartsInput struct {
	clispec.InputMarker
	Org            string `json:"org"`
	EventID        string `json:"event_id"`
	VerifyPayloads bool   `json:"verify_payloads,omitempty"`
}

// auditManifestPartsResult represents the command's JSON result.
type auditManifestPartsResult struct {
	clispec.ResultMarker
	Command   string                   `json:"command"`
	OrgID     uuid.UUID                `json:"org_id"`
	EventID   uuid.UUID                `json:"event_id"`
	PartCount int                      `json:"part_count"`
	Parts     []auditManifestPartEntry `json:"parts"`
}

var _ clispec.ReadResult = (*auditManifestPartsResult)(nil)

// auditManifestPartEntry represents one part index and digest in the JSON
// result.
type auditManifestPartEntry struct {
	Index  uint32 `json:"index"`
	Digest string `json:"digest"`
}

// AuditReadTargets returns one read target for each returned part.
//
// Each resource target URI combines EventID and the part's decimal Index.
// The method returns targets in the order of Parts and returns a nil error.
//
// Each URI uses this layout:
//
//	tack:audit-event/<eventID>/part/<index>
//
// For example, a caller can read the targets:
//
//	targets, err := r.AuditReadTargets()
func (r auditManifestPartsResult) AuditReadTargets() ([]audit.Target, error) {
	targets := make([]audit.Target, 0, len(r.Parts))
	for _, part := range r.Parts {
		uri := audit.EventURIPrefix + r.EventID.String() + auditEventPartURISegment + strconv.FormatUint(
			uint64(part.Index),
			10,
		)
		targets = append(targets, audit.ResourceTarget(uri))
	}

	return targets, nil
}

type auditManifestVerificationResult struct {
	clispec.ResultMarker
	Command      string                     `json:"command"`
	OrgID        uuid.UUID                  `json:"org_id"`
	EventID      uuid.UUID                  `json:"event_id"`
	Verification audit.ManifestVerification `json:"verification"`
}

func (r auditManifestVerificationResult) AuditReadTargets() ([]audit.Target, error) {
	return []audit.Target{audit.EventTarget(r.EventID)}, nil
}

// auditManifestPartsOp declares the command in the ops audit group.
func auditManifestPartsOp(f *cli.Factory) clispec.Operation[auditManifestPartsInput] {
	return clispec.Operation[auditManifestPartsInput]{
		Name:     clispec.Name{Canonical: "manifest-parts", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    auditManifestPartsSpec(),
		Group:    auditOpsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Print audit event part indexes and digests.",
		Long:     "Read part indexes and digests without printing payloads. A part count of 0 applies only to the org in --org and exits successfully. With --verify-payloads, check each payload and the declared manifest root and count. This check does not verify a signature. Run the command through the audit-ops service.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditManifestPartsInput]{
			clispec.StringParam(
				"org",
				"UUID of the org that owns the audit event.",
				"",
				true,
				func(in *auditManifestPartsInput, v string) { in.Org = v },
			),
			clispec.StringParam(
				"event-id",
				"UUID of the audit event.",
				"",
				true,
				func(in *auditManifestPartsInput, v string) { in.EventID = v },
			),
			clispec.BoolParam(
				"verify-payloads",
				"Verify stored manifest payloads against the event declaration.",
				false,
				func(in *auditManifestPartsInput, v bool) { in.VerifyPayloads = v },
			),
		},
		New: func() auditManifestPartsInput {
			return auditManifestPartsInput{
				InputMarker:    clispec.InputMarker{},
				Org:            "",
				EventID:        "",
				VerifyPayloads: false,
			}
		},
		Run: func(ctx context.Context, in auditManifestPartsInput, sink clispec.ResultSink) error {
			return runAuditManifestParts(ctx, f, in, sink)
		},
	}
}

// runAuditManifestParts reads and writes manifest part indexes and digests
// without loading payloads.
func runAuditManifestParts(
	ctx context.Context,
	f *cli.Factory,
	in auditManifestPartsInput,
	sink clispec.ResultSink,
) error {
	orgID, err := parseAuditManifestUUID(ctx, "--org", in.Org)
	if err != nil {
		return err
	}

	eventID, err := parseAuditManifestUUID(ctx, "--event-id", in.EventID)
	if err != nil {
		return err
	}

	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		// Omit the reader error, which can contain text from AUDIT_READER_DSN.
		slog.ErrorContext(
			ctx,
			"audit.manifest_parts_reader_failed",
			slog.String("err", "the ledger reader did not open"),
		)

		return fmt.Errorf(
			"%s: open the ledger reader: check AUDIT_READER_DSN",
			auditManifestPartsCommand,
		)
	}
	defer reader.Close()
	if in.VerifyPayloads {
		verification, err := reader.VerifyManifest(ctx, orgID, eventID)
		if err != nil {
			return fmt.Errorf("%s: verify event %s: %w", auditManifestPartsCommand, eventID, err)
		}

		result := auditManifestVerificationResult{
			ResultMarker: clispec.ResultMarker{},
			Command:      "ops.audit.manifest_parts",
			OrgID:        orgID,
			EventID:      eventID,
			Verification: verification,
		}
		if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
			return fmt.Errorf("%s: write the verification: %w", auditManifestPartsCommand, err)
		}

		return nil
	}

	parts, err := reader.ManifestPartDigests(ctx, orgID, eventID)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.manifest_parts_failed",
			slog.String("event_id", eventID.String()),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("%s %s: %w", auditManifestPartsCommand, eventID, err)
	}

	entries := make([]auditManifestPartEntry, 0, len(parts))
	for _, part := range parts {
		entries = append(entries, auditManifestPartEntry{Index: part.Index, Digest: part.Digest})
	}

	slog.InfoContext(
		ctx,
		"audit.manifest_parts_read",
		slog.String("event_id", eventID.String()),
		slog.Int("part_count", len(entries)),
	)
	result := auditManifestPartsResult{
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.audit.manifest_parts",
		OrgID:        orgID,
		EventID:      eventID,
		PartCount:    len(entries),
		Parts:        entries,
	}
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(
			ctx,
			"audit.manifest_parts_report_failed",
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("%s: write the result: %w", auditManifestPartsCommand, err)
	}

	return nil
}
