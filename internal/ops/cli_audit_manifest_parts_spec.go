package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

// parseAuditManifestUUID parses a flag value as a UUID. Its error identifies the flag and omits the value.
func parseAuditManifestUUID(ctx context.Context, flag, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		slog.ErrorContext(ctx, "audit.manifest_parts_bad_flag", slog.String("flag", flag), slog.String("err", "the value is not a UUID"))
		return uuid.Nil, fmt.Errorf("%s: %s must be a UUID", auditManifestPartsCommand, flag)
	}
	return id, nil
}

// canonicalAuditUUID returns the canonical form of a UUID. It returns an empty string when the value does not parse.
func canonicalAuditUUID(value string) string {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return id.String()
}

// decodeAuditManifestInput decodes the encoded command input.
func decodeAuditManifestInput(input json.RawMessage) (auditManifestPartsInput, error) {
	in := auditManifestPartsInput{InputMarker: clispec.InputMarker{}, Org: "", EventID: "", VerifyPayloads: false}
	if err := json.Unmarshal(input, &in); err != nil {
		slog.Error("audit.manifest_parts_input_failed", slog.String("err", err.Error()))
		return in, fmt.Errorf("%s: decode the command input: %w", auditManifestPartsCommand, err)
	}
	return in, nil
}

// auditManifestPartsSafeInput returns canonical UUID flag values for the audit event. It returns an empty string for each malformed value.
func auditManifestPartsSafeInput(input json.RawMessage) (json.RawMessage, error) {
	in, err := decodeAuditManifestInput(input)
	if err != nil {
		return nil, err
	}
	safe, err := json.Marshal(auditManifestPartsInput{
		InputMarker: clispec.InputMarker{}, Org: canonicalAuditUUID(in.Org), EventID: canonicalAuditUUID(in.EventID), VerifyPayloads: in.VerifyPayloads,
	})
	if err != nil {
		slog.Error("audit.manifest_parts_input_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s: encode the safe input: %w", auditManifestPartsCommand, err)
	}
	return safe, nil
}

// auditManifestPartsSpec declares an audited read with a target function and a safe input function that stores only parsed UUID flag values.
func auditManifestPartsSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbAuditRead), Reads: true, Class: audit.OperationRead,
		Targets:   auditManifestPartsTargets,
		SafeInput: auditManifestPartsSafeInput,
	}
}

// auditManifestPartsTargets returns the audited event target. It returns the fixed target when the event ID does not parse.
func auditManifestPartsTargets(_ context.Context, input json.RawMessage) ([]audit.Target, error) {
	in, err := decodeAuditManifestInput(input)
	if err != nil {
		return nil, err
	}
	eventID := canonicalAuditUUID(in.EventID)
	if eventID == "" {
		return []audit.Target{auditResourceTarget(auditEventInvalidURI)}, nil
	}
	return []audit.Target{auditResourceTarget(auditEventURIPrefix + eventID)}, nil
}
