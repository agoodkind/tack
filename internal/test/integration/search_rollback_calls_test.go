package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/ops"
)

// rolloutSearchPage represents one parsed tack_search response.
type rolloutSearchPage struct {
	ids      []uuid.UUID
	cursor   string
	complete bool
}

// searchAs calls tack_search through the production HTTP handler as the
// workspace's first actor and fails the test on an error result.
func searchAs(t *testing.T, fixture rolloutFixture, workspace datagen.WorkspaceIdentity, query, cursor string) rolloutSearchPage {
	t.Helper()
	arguments := map[string]any{"workspace_reference": workspace.Slug, "query": query}
	if cursor != "" {
		arguments["cursor"] = cursor
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": uuid.NewString(), "method": "tools/call",
		"params": map[string]any{"name": "tack_search", "arguments": arguments},
	})
	if err != nil {
		t.Fatalf("cannot encode search request: %v", err)
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+workspace.Actors[0].Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("search returned HTTP status %d with read error %v and body %s", response.StatusCode, err, payload)
	}
	for _, line := range bytes.Split(payload, []byte("\n")) {
		if data, found := bytes.CutPrefix(line, []byte("data: ")); found {
			payload = data
		}
	}
	var decoded struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Result.IsError || len(decoded.Result.Content) == 0 {
		t.Fatalf("search for %q did not return a usable result: decode error %v, payload %s", query, err, payload)
	}
	return parseRolloutSearchPage(t, decoded.Result.Content[0].Text)
}

// parseRolloutSearchPage reads result IDs, the next cursor, and
// completion from the response.
func parseRolloutSearchPage(t *testing.T, text string) rolloutSearchPage {
	t.Helper()
	page := rolloutSearchPage{ids: []uuid.UUID{}, cursor: "", complete: strings.Contains(text, "The search has no more results.")}
	for _, line := range strings.Split(text, "\n") {
		if marker := strings.LastIndex(line, "Raw id: `"); marker >= 0 {
			id, err := uuid.Parse(strings.TrimSuffix(line[marker+len("Raw id: `"):], "`"))
			if err != nil {
				t.Fatalf("cannot parse result ID from line %q: %v", line, err)
			}
			page.ids = append(page.ids, id)
		}
		if cursor, found := strings.CutPrefix(line, "Next cursor: `"); found {
			page.cursor = strings.TrimSuffix(cursor, "`")
		}
	}
	if page.complete == (page.cursor != "") {
		t.Fatalf("search page must contain either a cursor or completion, but not both: %s", text)
	}
	return page
}

// traverseSearch follows the cursor in page until the search completes
// and returns the IDs from page and every later page.
func traverseSearch(t *testing.T, fixture rolloutFixture, workspace datagen.WorkspaceIdentity, query string, page rolloutSearchPage) []uuid.UUID {
	t.Helper()
	ids := append([]uuid.UUID{}, page.ids...)
	for range 1000 {
		if page.complete {
			return ids
		}
		page = searchAs(t, fixture, workspace, query, page.cursor)
		ids = append(ids, page.ids...)
	}
	t.Fatalf("search for %q did not complete within 1000 pages", query)
	return nil
}

// sessionAccessVersion decodes the cursor with the configured key and
// returns its stored session's access version.
func sessionAccessVersion(t *testing.T, fixture rolloutFixture, cursor string) string {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(os.Getenv("OPENSEARCH_CURSOR_KEY"))
	if err != nil {
		t.Fatalf("cannot decode cursor key: %v", err)
	}
	sessionID, _, err := tools.NewSearchCursorCodec(key).Decode(cursor)
	if err != nil {
		t.Fatalf("cannot decode cursor: %v", err)
	}
	session, err := fixture.stores.SearchSessions(clock.Wall{}, 15*time.Minute).Load(rolloutContext(t), sessionID)
	if err != nil {
		t.Fatalf("cannot load session %s: %v", sessionID, err)
	}
	return session.Query.Access.Version
}

// runAccessRollback executes rollback for authorityID through the audited
// command tree with the real SQL outbox and a new operator ID. It returns
// the operator ID and command error.
func runAccessRollback(t *testing.T, fixture rolloutFixture, authorityID uuid.UUID) (uuid.UUID, error) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), fixture.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("cannot open outbox pool: %v", err)
	}
	t.Cleanup(pool.Close)
	operatorID := uuid.Must(uuid.NewV7())
	output := &bytes.Buffer{}
	factory := cli.System(fixture.cfg)
	factory.Out = output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	root.SetContext(t.Context())
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", operatorID.String(),
		"--operator-email", "operator@example.com", "--operator-name", "Access Rollback Test",
		"ops", "search", "access-rollback", "--authority", authorityID.String(),
	})
	err = root.Execute()
	t.Logf("rollback for authority %s returned error %v and output %s", authorityID, err, output.String())
	return operatorID, err
}

// requireRollbackAudit requires one pending intent row and one outcome
// row with outcome for the rollback verb under operatorID.
func requireRollbackAudit(t *testing.T, fixture rolloutFixture, operatorID uuid.UUID, outcome audit.Outcome) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), fixture.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("cannot open outbox pool: %v", err)
	}
	t.Cleanup(pool.Close)
	rows, err := audit.NewPoolOutbox(pool).ReadBatch(rolloutContext(t), 1000)
	if err != nil {
		t.Fatalf("cannot read operator outbox: %v", err)
	}
	outcomes := make(map[audit.Outcome]int)
	for _, row := range rows {
		if row.Event.Verb == string(audit.VerbOpsSearchAccessRollback) && row.Event.Actor.ID == operatorID {
			outcomes[row.Event.Outcome]++
		}
	}
	if outcomes[audit.OutcomePending] != 1 || outcomes[outcome] != 1 || len(outcomes) != 2 {
		t.Fatalf("rollback audit outcome counts %v must include one pending intent and one %s outcome", outcomes, outcome)
	}
}
