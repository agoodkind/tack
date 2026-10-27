package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
)

// gettingStartedURI is the resource URI that tools.RegisterResources serves.
const gettingStartedURI = "tack://getting-started"

// readGettingStarted registers the resources for resolver on a new MCP server
// and reads the getting-started resource through the JSON-RPC surface with
// ctx. It fails the test unless the read returns one resource.
func readGettingStarted(t *testing.T, ctx context.Context, resolver *tools.Resolver) {
	t.Helper()
	server := mcpserver.NewMCPServer("tack", "0.2.0")
	tools.RegisterResources(server, nil, resolver, nil, nil)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "resources/read",
		"params": map[string]any{"uri": gettingStartedURI},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	encoded, err := json.Marshal(server.HandleMessage(ctx, body))
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var response struct {
		Result struct {
			Contents []json.RawMessage `json:"contents"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, encoded)
	}
	if response.Error != nil || len(response.Result.Contents) != 1 {
		t.Fatalf("resources/read = %s, want the one markdown document", encoded)
	}
}

// resourceReadEvent returns the one mcp.resource_read event in recorder.
func resourceReadEvent(t *testing.T, recorder *audit.MemoryRecorder) audit.Event {
	t.Helper()
	var found []audit.Event
	for _, event := range recorder.Events() {
		if event.Verb == string(audit.VerbMCPResourceRead) {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("recorder stored %d mcp.resource_read events, want 1: %+v", len(found), recorder.Events())
	}
	return found[0]
}

// TestResourceReadRecordsTheRead pins that reading the getting-started
// resource records an mcp.resource_read event through the recorder on the
// request context. The handler used to take a context and ignore it, and no
// code emitted mcp.resource_read (TACK-340).
func TestResourceReadRecordsTheRead(t *testing.T) {
	recorder := audit.NewMemoryRecorder()
	actorID := uuid.New()
	ctx := tools.WithAuditRecorder(auth.WithUser(t.Context(), actorID), recorder)

	readGettingStarted(t, ctx, tools.NewResolver(nil, nil, nil, nil))

	event := resourceReadEvent(t, recorder)
	if event.Actor.ID != actorID || event.Entity.Type != "mcp_resource" || event.Entity.Name != gettingStartedURI {
		t.Fatalf("mcp.resource_read actor = %s, entity = %s %q; want %s, mcp_resource %q",
			event.Actor.ID, event.Entity.Type, event.Entity.Name, actorID, gettingStartedURI)
	}
}

// TestResourceReadCarriesTheReadersSoleOrg is TACK-477. A resource read does
// not resolve a workspace, and the event used to carry the nil org. A reader
// with exactly one org gets that org, and a reader with zero or two orgs gets
// the nil org.
func TestResourceReadCarriesTheReadersSoleOrg(t *testing.T) {
	orgID := uuid.New()
	cases := map[string]struct {
		orgs []uuid.UUID
		want uuid.UUID
	}{
		"one":  {orgs: []uuid.UUID{orgID}, want: orgID},
		"two":  {orgs: []uuid.UUID{orgID, uuid.New()}, want: uuid.Nil},
		"none": {orgs: nil, want: uuid.Nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := audit.NewMemoryRecorder()
			// The request attaches its membership as the auth middleware does.
			// The resolver reads the membership from the context and does not
			// query the membership repository.
			ctx := auth.WithOrgMembership(auth.WithUser(t.Context(), uuid.New()), tc.orgs)
			ctx = tools.WithAuditRecorder(ctx, recorder)

			readGettingStarted(t, ctx, tools.NewResolver(nil, nil, postgres.NewOrgMemberRepo(nil), nil))

			if got := resourceReadEvent(t, recorder).Context.OrgID; got != tc.want {
				t.Fatalf("resource_read org = %s, want %s", got, tc.want)
			}
		})
	}
}
