package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

const (
	// auditManifestPartsCommand identifies the command in errors and logs.
	auditManifestPartsCommand = "ops audit manifest-parts"
	// auditEventURIPrefix is the URI prefix for an audited event target.
	auditEventURIPrefix = "tack:audit-event/"
	// auditEventPartURISegment is the URI segment before a part index.
	auditEventPartURISegment = "/part/"
)

// auditManifestPartsInput stores the flag values that the audit event records as safe input.
type auditManifestPartsInput struct {
	clispec.InputMarker
	Org     string `json:"org"`
	EventID string `json:"event_id"`
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

// auditManifestPartEntry represents one part index and digest in the JSON result.
type auditManifestPartEntry struct {
	Index  uint32 `json:"index"`
	Digest string `json:"digest"`
}

// AuditReadTargets returns one read target for each returned part.
func (r auditManifestPartsResult) AuditReadTargets() ([]audit.Target, error) {
	targets := make([]audit.Target, 0, len(r.Parts))
	for _, part := range r.Parts {
		uri := auditEventURI(r.EventID) + auditEventPartURISegment + strconv.FormatUint(uint64(part.Index), 10)
		targets = append(targets, auditResourceTarget(uri))
	}
	return targets, nil
}

// auditEventURI returns the resource URI for an audited event.
func auditEventURI(eventID uuid.UUID) string {
	return auditEventURIPrefix + eventID.String()
}

// auditResourceTarget returns a resource target for a URI.
func auditResourceTarget(uri string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "", URI: uri,
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// parseAuditManifestUUID parses one flag value as a UUID.
func parseAuditManifestUUID(ctx context.Context, flag, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		slog.ErrorContext(ctx, "audit.manifest_parts_bad_flag", slog.String("flag", flag), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("%s: %s must be a UUID: %w", auditManifestPartsCommand, flag, err)
	}
	return id, nil
}

// auditManifestPartsSpec declares the typed read. SafeInput returns the input unchanged. The input contains only --org and --event-id.
func auditManifestPartsSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbAuditRead), Reads: true, Class: audit.OperationRead,
		Targets:   auditManifestPartsTargets,
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
}

// auditManifestPartsTargets decodes the input and returns the audited event target.
func auditManifestPartsTargets(ctx context.Context, input json.RawMessage) ([]audit.Target, error) {
	var in auditManifestPartsInput
	if err := json.Unmarshal(input, &in); err != nil {
		slog.ErrorContext(ctx, "audit.manifest_parts_input_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s: decode the command input: %w", auditManifestPartsCommand, err)
	}
	eventID, err := parseAuditManifestUUID(ctx, "--event-id", in.EventID)
	if err != nil {
		return nil, err
	}
	return []audit.Target{auditResourceTarget(auditEventURI(eventID))}, nil
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
		Long:     "Read part indexes and digests without printing payloads. A part count of 0 applies only to the org in --org and exits successfully. Run the command through the audit-ops service.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditManifestPartsInput]{
			clispec.StringParam("org", "UUID of the org that owns the audit event.", "", true, func(in *auditManifestPartsInput, v string) { in.Org = v }),
			clispec.StringParam("event-id", "UUID of the audit event.", "", true, func(in *auditManifestPartsInput, v string) { in.EventID = v }),
		},
		New: func() auditManifestPartsInput {
			return auditManifestPartsInput{InputMarker: clispec.InputMarker{}, Org: "", EventID: ""}
		},
		Run: func(ctx context.Context, in auditManifestPartsInput, sink clispec.ResultSink) error {
			return runAuditManifestParts(ctx, f, in, sink)
		},
	}
}

// runAuditManifestParts reads parts through the audit_reader role and writes the result without payloads.
func runAuditManifestParts(ctx context.Context, f *cli.Factory, in auditManifestPartsInput, sink clispec.ResultSink) error {
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
		slog.ErrorContext(ctx, "audit.manifest_parts_reader_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: open the ledger reader: %w", auditManifestPartsCommand, err)
	}
	defer reader.Close()
	parts, err := reader.ManifestParts(ctx, orgID, eventID)
	if err != nil {
		slog.ErrorContext(ctx, "audit.manifest_parts_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("%s %s: %w", auditManifestPartsCommand, eventID, err)
	}
	entries := make([]auditManifestPartEntry, 0, len(parts))
	for _, part := range parts {
		entries = append(entries, auditManifestPartEntry{Index: part.Index, Digest: part.Digest})
	}
	slog.InfoContext(ctx, "audit.manifest_parts_read", slog.String("event_id", eventID.String()), slog.Int("part_count", len(entries)))
	result := auditManifestPartsResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.audit.manifest_parts", OrgID: orgID, EventID: eventID,
		PartCount: len(entries), Parts: entries,
	}
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "audit.manifest_parts_report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: write the result: %w", auditManifestPartsCommand, err)
	}
	return nil
}
