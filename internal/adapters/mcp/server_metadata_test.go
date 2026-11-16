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

// capturingMembers records the membership lookup context and returns an error
// before the handler dispatches the tool call.
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

// TestServeHTTPPreservesRequestMetadataInMembershipContext checks the request's JSON-RPC
// ID and session ID in the membership lookup context. Create idempotency uses
// these IDs to identify retries (TACK-476).
func TestServeHTTPPreservesRequestMetadataInMembershipContext(t *testing.T) {
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
