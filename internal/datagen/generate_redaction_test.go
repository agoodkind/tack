package datagen

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAuditRedactionDryRunLogsPlanned(t *testing.T) {
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})
	generator := &Generator{
		driver:           NewDriver(nil, true, 245),
		dryRun:           true,
		redactAuditPII:   true,
		synchronousAudit: true,
		identities: Identities{Workspaces: []WorkspaceIdentity{{
			Actors: []Actor{{UserID: uuid.Nil, Token: "token"}},
		}}},
	}
	if err := generator.redactOneActor(t.Context()); err != nil {
		t.Fatalf("redactOneActor() error = %v", err)
	}
	logs := output.String()
	if !strings.Contains(logs, "qa.datagen.audit_redaction_planned") {
		t.Fatalf("dry-run logs = %q, want planned event", logs)
	}
	if strings.Contains(logs, "qa.datagen.audit_redaction_requested") {
		t.Fatalf("dry-run logs = %q, do not want requested event", logs)
	}
}

func TestAuditRedactionSkipsWhenToolIsNotRegistered(t *testing.T) {
	t.Parallel()
	requestCount := 0
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"jsonrpc":"2.0","id":"1","result":{"tools":[` +
				`{"name":"tack_list_workspaces"}]}}`,
		))
	})
	generator := redactionGenerator(handler)
	if err := generator.redactOneActor(t.Context()); err != nil {
		t.Fatalf("redactOneActor() error = %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want tools/list only", requestCount)
	}
}

func TestAuditRedactionTreatsToolNotFoundAsBestEffortSkip(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload requestMethod
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		if payload.Method == toolsList {
			_, _ = writer.Write([]byte(
				`{"jsonrpc":"2.0","id":"1","result":{"tools":[` +
					`{"name":"tack_audit_redact_actor"}]}}`,
			))
			return
		}
		_, _ = writer.Write([]byte(
			`{"jsonrpc":"2.0","id":"1","error":` +
				`{"code":-32601,"message":"tool not found"}}`,
		))
	})
	generator := redactionGenerator(handler)
	if err := generator.redactOneActor(t.Context()); err != nil {
		t.Fatalf("redactOneActor() error = %v", err)
	}
}

type requestMethod struct {
	Method string `json:"method"`
}

func redactionGenerator(handler http.Handler) *Generator {
	return &Generator{
		driver:           NewDriver(testGraph(handler), false, 245),
		redactAuditPII:   true,
		synchronousAudit: true,
		identities: Identities{Workspaces: []WorkspaceIdentity{{
			Actors: []Actor{{UserID: uuid.Nil, Token: "token"}},
		}}},
	}
}
