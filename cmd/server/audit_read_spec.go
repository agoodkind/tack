package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

const (
	auditQueryCommand = "audit query"
	auditGetCommand   = "audit get"

	auditLastChangeCommand = "audit last-change"
)

func auditQuerySpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbAuditRead), Reads: true, Class: audit.OperationRead,
		Targets: auditQueryTargets, SafeInput: auditQuerySafeInput,
	}
}

func auditGetSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbAuditRead), Reads: true, Class: audit.OperationRead,
		Targets: auditGetTargets, SafeInput: auditGetSafeInput,
	}
}

func canonicalAuditTime(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return parsed.Format(time.RFC3339)
}

func auditLastChangeSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbAuditRead), Reads: true, Class: audit.OperationRead,
		Targets: auditLastChangeTargets, SafeInput: auditLastChangeSafeInput,
	}
}

func auditLastChangeTargets(_ context.Context, encoded json.RawMessage) ([]audit.Target, error) {
	change, err := decodeAuditReadInput[auditLastChangeInput](auditLastChangeCommand, encoded)
	if err != nil {
		return nil, err
	}
	return []audit.Target{audit.ResourceTarget(audit.ResourceURI(audit.NodeURIPrefix, change.EntityID))}, nil
}

func auditLastChangeSafeInput(encoded json.RawMessage) (json.RawMessage, error) {
	change, err := decodeAuditReadInput[auditLastChangeInput](auditLastChangeCommand, encoded)
	if err != nil {
		return nil, err
	}
	safe, err := json.Marshal(auditLastChangeInput{
		InputMarker: clispec.InputMarker{}, Org: audit.CanonicalUUID(change.Org), EntityID: audit.CanonicalUUID(change.EntityID),
		Oldest: canonicalAuditTime(change.Oldest), Latest: canonicalAuditTime(change.Latest), Path: change.Path, Cursor: "",
	})
	if err != nil {
		slog.Error("audit.read_input_failed", slog.String("command", auditLastChangeCommand), slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s: encode the safe input: %w", auditLastChangeCommand, err)
	}
	return safe, nil
}

func decodeAuditReadInput[Input auditQueryInput | auditGetInput | auditLastChangeInput](command string, encoded json.RawMessage) (Input, error) {
	var decoded Input
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		slog.Error("audit.read_input_failed", slog.String("command", command), slog.String("err", err.Error()))
		return decoded, fmt.Errorf("%s: decode the command input: %w", command, err)
	}
	return decoded, nil
}

func auditQueryTargets(_ context.Context, encoded json.RawMessage) ([]audit.Target, error) {
	query, err := decodeAuditReadInput[auditQueryInput](auditQueryCommand, encoded)
	if err != nil {
		return nil, err
	}
	return []audit.Target{audit.ResourceTarget(audit.ResourceURI(audit.AuditOrgURIPrefix, query.Org))}, nil
}

func auditQuerySafeInput(encoded json.RawMessage) (json.RawMessage, error) {
	query, err := decodeAuditReadInput[auditQueryInput](auditQueryCommand, encoded)
	if err != nil {
		return nil, err
	}
	safe, err := json.Marshal(auditQueryInput{
		InputMarker: clispec.InputMarker{}, Org: audit.CanonicalUUID(query.Org), Oldest: canonicalAuditTime(query.Oldest),
		Latest: canonicalAuditTime(query.Latest), Action: query.Action, ActorID: audit.CanonicalUUID(query.ActorID),
		EntityID: audit.CanonicalUUID(query.EntityID), RequestID: query.RequestID, TraceID: query.TraceID,
		Limit: query.Limit, Cursor: "",
	})
	if err != nil {
		slog.Error("audit.read_input_failed", slog.String("command", auditQueryCommand), slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s: encode the safe input: %w", auditQueryCommand, err)
	}
	return safe, nil
}

func auditGetTargets(_ context.Context, encoded json.RawMessage) ([]audit.Target, error) {
	get, err := decodeAuditReadInput[auditGetInput](auditGetCommand, encoded)
	if err != nil {
		return nil, err
	}
	return []audit.Target{audit.ResourceTarget(audit.ResourceURI(audit.EventURIPrefix, get.EventID))}, nil
}

func auditGetSafeInput(encoded json.RawMessage) (json.RawMessage, error) {
	get, err := decodeAuditReadInput[auditGetInput](auditGetCommand, encoded)
	if err != nil {
		return nil, err
	}
	safe, err := json.Marshal(auditGetInput{InputMarker: clispec.InputMarker{}, EventID: audit.CanonicalUUID(get.EventID)})
	if err != nil {
		slog.Error("audit.read_input_failed", slog.String("command", auditGetCommand), slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s: encode the safe input: %w", auditGetCommand, err)
	}
	return safe, nil
}
