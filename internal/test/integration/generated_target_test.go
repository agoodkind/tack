package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

func TestRuntimeAuditMutationSelection(t *testing.T) {
	server, ledger := startRecordedPublicServer(t)
	identifier := "SEL" + strings.ToUpper(uuid.NewString()[:5])
	server.createNode(
		t,
		"tack_create_project",
		map[string]any{
			"workspace_reference": server.workspace,
			"name":                identifier,
			"properties":          map[string]any{"identifier": identifier},
		},
	)
	issueID := server.createNode(
		t,
		"tack_create_issue",
		map[string]any{
			"workspace_reference": server.workspace,
			"project_reference":   identifier,
			"name":                "Selection issue",
		},
	)

	setterID := newRequestID(t, "setter-selection")
	setter := server.rpc(
		t,
		setterID,
		"tools/call",
		map[string]any{
			"name": "tack_set_issue_state",
			"arguments": map[string]any{
				"workspace_reference": server.workspace,
				"project_reference":   identifier,
				"issue_reference":     issueID.String(),
				"state":               "Done",
			},
		},
	)
	toolText(t, setter)
	requireMutationSelection(t, ledger, server, setterID, "tack_set_issue_state", issueID.String())

	updateID := newRequestID(t, "update-selection")
	update := server.rpc(
		t,
		updateID,
		"tools/call",
		map[string]any{
			"name": "tack_update_issue",
			"arguments": map[string]any{
				"workspace_reference": server.workspace,
				"node_id":             issueID.String(),
				"name":                "Updated selection issue",
				"source_id":           uuid.NewString(),
				"target_id":           uuid.NewString(),
				"relation_type":       "watches",
			},
		},
	)
	var response struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if update.status != http.StatusOK || json.Unmarshal(update.body, &response) != nil ||
		(len(response.Error) == 0 && !response.Result.IsError) {
		t.Fatalf(
			"update with unknown arguments returned status %d and body %s",
			update.status,
			update.body,
		)
	}

	requireMutationSelection(t, ledger, server, updateID, "tack_update_issue", issueID.String())
}

func requireMutationSelection(
	t *testing.T,
	ledger publicLedger,
	server *publicServer,
	requestID string,
	tool string,
	reference string,
) {
	t.Helper()
	rows := ledger.requestRows(
		t,
		server.orgID,
		requestID,
		func(rows []audit.Row) bool {
			return hasMutationAttempt(rows, audit.VerbNodeUpdate) && hasAuthAndOther(rows)
		},
	)
	auth := requireTokenUsedRow(t, rows, server.userID)
	requireMutationAttempt(
		t,
		rows,
		auth,
		requestID,
		audit.VerbNodeUpdate,
		tool,
		reference,
		nil,
		server.workspace,
	)

	for _, row := range rows {
		if row.Action != string(audit.VerbNodeUpdate) || row.Context.Phase != audit.PhaseAttempt {
			continue
		}

		if row.Context.Target == nil || row.Context.Target.Kind != audit.TargetKindResource ||
			row.Context.Target.URI != "/mcp" || row.Context.Selection == nil {
			t.Fatalf(
				"attempt %s target = %+v, selection = %+v",
				row.EventID,
				row.Context.Target,
				row.Context.Selection,
			)
		}

		for _, key := range []string{
			"state",
			"name",
			"properties",
			"source_id",
			"target_id",
			"relation_type",
		} {
			if _, found := row.Context.Selection.Criteria[key]; found {
				t.Fatalf("attempt %s records unexpected selection field %s", row.EventID, key)
			}
		}

		return
	}

	t.Fatalf("request %s did not record a node update attempt", requestID)
}
