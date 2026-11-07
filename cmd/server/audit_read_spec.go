package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

const (
	auditEventURIPrefix = "tack:audit-event/"
	auditOrgURIPrefix   = "tack:audit-org/"
	auditInvalidSegment = "invalid"
	auditQueryCommand   = "audit query"
	auditGetCommand     = "audit get"
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

func auditResourceTarget(uri string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "", URI: uri,
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

func canonicalAuditUUID(value string) string {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return id.String()
}

func canonicalAuditTime(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return parsed.Format(time.RFC3339)
}

func auditResourceURI(prefix, value string) string {
	id := canonicalAuditUUID(value)
	if id == "" {
		return prefix + auditInvalidSegment
	}
	return prefix + id
}

func decodeAuditReadInput[Input auditQueryInput | auditGetInput](command string, encoded json.RawMessage) (Input, error) {
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
	return []audit.Target{auditResourceTarget(auditResourceURI(auditOrgURIPrefix, query.Org))}, nil
}

func auditQuerySafeInput(encoded json.RawMessage) (json.RawMessage, error) {
	query, err := decodeAuditReadInput[auditQueryInput](auditQueryCommand, encoded)
	if err != nil {
		return nil, err
	}
	safe, err := json.Marshal(auditQueryInput{
		InputMarker: clispec.InputMarker{}, Org: canonicalAuditUUID(query.Org), Oldest: canonicalAuditTime(query.Oldest),
		Latest: canonicalAuditTime(query.Latest), Action: query.Action, ActorID: canonicalAuditUUID(query.ActorID),
		EntityID: canonicalAuditUUID(query.EntityID), RequestID: query.RequestID, TraceID: query.TraceID,
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
	return []audit.Target{auditResourceTarget(auditResourceURI(auditEventURIPrefix, get.EventID))}, nil
}

func auditGetSafeInput(encoded json.RawMessage) (json.RawMessage, error) {
	get, err := decodeAuditReadInput[auditGetInput](auditGetCommand, encoded)
	if err != nil {
		return nil, err
	}
	safe, err := json.Marshal(auditGetInput{InputMarker: clispec.InputMarker{}, EventID: canonicalAuditUUID(get.EventID)})
	if err != nil {
		slog.Error("audit.read_input_failed", slog.String("command", auditGetCommand), slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s: encode the safe input: %w", auditGetCommand, err)
	}
	return safe, nil
}
