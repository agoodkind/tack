package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/org"
)

// capturingMembers saves the context from the membership lookup after the
// handler reads request metadata. It returns an error before the handler
// builds or serves the MCP server.
type capturingMembers struct {
	seen context.Context
}

func (m *capturingMembers) AddMember(context.Context, *org.Member) error {
	panic("capturingMembers.AddMember called")
}

func (m *capturingMembers) RemoveMember(context.Context, uuid.UUID, uuid.UUID) error {
	panic("capturingMembers.RemoveMember called")
}

func (m *capturingMembers) ListMembers(context.Context, uuid.UUID) ([]*org.Member, error) {
	panic("capturingMembers.ListMembers called")
}

func (m *capturingMembers) ListOrgIDsForUser(
	ctx context.Context,
	_ uuid.UUID,
) ([]uuid.UUID, error) {
	m.seen = ctx
	return nil, errors.New("stop here")
}

// TestServeHTTPCarriesRequestMetadataDownstream checks that the membership
// lookup context contains the JSON-RPC request ID and session ID from the
// request. Create idempotency reads these context values when it uses a
// session-scoped request ID. This test stops at membership lookup and does
// not exercise create retries.
//
// TACK-476 added this regression for an earlier ServeHTTP implementation
// that discarded parsed request metadata when rebuilding the context.
// Preserving that identity supports the requirement that a retried create
// reuse its node.
func TestServeHTTPCarriesRequestMetadataDownstream(t *testing.T) {
	members := &capturingMembers{seen: nil}
	handler := NewHandler(Deps{
		NodeSvc:       nil,
		Nodes:         nil,
		Reader:        nil,
		NodeTypes:     nil,
		PropertyDefs:  nil,
		Relationships: nil,
		Members:       members,
		Users:         nil,
	})
	body := `{"jsonrpc":"2.0","id":"datagen-abc","method":"tools/call","params":{"name":"tack_create_issue"}}`
	request := httptest.NewRequestWithContext(
		auth.WithUser(context.Background(), uuid.New()),
		http.MethodPost,
		"/mcp",
		strings.NewReader(body),
	)
	request.Header.Set("Mcp-Session-Id", "qa-datagen-9130")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf(
			"status = %d, want %d from the membership failure",
			recorder.Code,
			http.StatusInternalServerError,
		)
	}

	if members.seen == nil {
		t.Fatal("the membership lookup never ran")
	}

	metadata, ok := tools.MCPRequestMetadataFromContext(members.seen)
	if !ok || metadata.RequestID != "datagen-abc" || metadata.SessionID != "qa-datagen-9130" {
		t.Fatalf(
			"metadata = %+v (present %v), want the request id and session id on the downstream context",
			metadata,
			ok,
		)
	}
}
