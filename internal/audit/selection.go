package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

var errSelectionUnsupported = errors.New("command does not declare selected audit input")

// ExportSelection identifies the requested organization and export filters.
type ExportSelection struct {
	OrgID     string `json:"org_id"`
	Oldest    string `json:"oldest"`
	Latest    string `json:"latest"`
	RequestID string `json:"request_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	Limit     int    `json:"limit"`
}

// SelectCommandInput selects requested export filters or a token UUID.
//
// The selection excludes export output and invalid token values. The command
// still validates its input before execution.
//
//	selected, err := SelectCommandInput(verb, encoded)
func SelectCommandInput(verb Verb, encoded json.RawMessage) (json.RawMessage, error) {
	if verb == VerbAuditExport {
		return marshalExportInput(encoded)
	}

	if verb == VerbOpsAuthTokenRevoke {
		return marshalRevokeInput(encoded)
	}

	return selectionError(
		"select command input",
		fmt.Errorf("audit operation %q: %w", verb, errSelectionUnsupported),
	)
}

func marshalExportInput(encoded json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Org       string `json:"Org"`
		Oldest    string `json:"Oldest"`
		Latest    string `json:"Latest"`
		RequestID string `json:"RequestID"`
		TraceID   string `json:"TraceID"`
		Limit     int    `json:"Limit"`
	}
	if err := json.Unmarshal(encoded, &input); err != nil {
		return selectionError("decode export selection", err)
	}

	selected, err := json.Marshal(ExportSelection{
		OrgID:     input.Org,
		Oldest:    input.Oldest,
		Latest:    input.Latest,
		RequestID: input.RequestID,
		TraceID:   input.TraceID,
		Limit:     input.Limit,
	})
	if err != nil {
		return selectionError("encode export selection", err)
	}

	return selected, nil
}

func marshalRevokeInput(encoded json.RawMessage) (json.RawMessage, error) {
	var input struct {
		TokenID string `json:"TokenID"`
	}
	if err := json.Unmarshal(encoded, &input); err != nil {
		return selectionError("decode requested token ID", err)
	}

	var selection struct {
		TokenID *uuid.UUID `json:"token_id,omitempty"`
	}
	tokenID, parseErr := uuid.Parse(input.TokenID)
	if parseErr == nil {
		selection.TokenID = &tokenID
	}

	selected, err := json.Marshal(selection)
	if err != nil {
		return selectionError("encode requested token ID", err)
	}

	return selected, nil
}

func selectionError(action string, cause error) (json.RawMessage, error) {
	slog.Error(
		"audit.input_failed",
		slog.String("operation", action),
		slog.String("err", cause.Error()),
	)

	return nil, fmt.Errorf("%s: %w", action, cause)
}
