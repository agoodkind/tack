package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	appruntime "goodkind.io/tack/internal/runtime"
)

const (
	rankingProject  = "RANK"
	rankingMaxPages = 1000
)

type rankingRun struct {
	driver    *datagen.Driver
	token     string
	workspace string
	stores    *fdbadapter.Stores
	client    *opensearchapi.Client
	cursors   *tools.SearchCursorCodec
	query     string
	target    uuid.UUID
	names     map[uuid.UUID]string
}

type rankingPage struct {
	IDs      []uuid.UUID
	Cursor   string
	Complete bool
}

func newRankingRun(t *testing.T, cfg *config.Config, graph *appruntime.Graph) *rankingRun {
	t.Helper()
	seed := clock.Now().UnixNano()
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(t.Context(), cfg, seed, scale)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open ranking stores: %v", err)
	}
	certificate, err := config.LoadSearchCA(t.Context(), cfg.SearchCA)
	if err != nil {
		t.Fatalf("load search CA: %v", err)
	}
	settings := search.Config{
		Endpoint: cfg.SearchEndpoint, CA: certificate, Username: cfg.SearchUsername,
		Password: cfg.SearchPassword, RequestTimeout: 2 * time.Minute, MaxRetries: 1,
	}
	base, err := opensearch.NewClient(settings.ClientConfig(nil))
	if err != nil {
		t.Fatalf("open OpenSearch client: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv("OPENSEARCH_CURSOR_KEY"))
	if err != nil {
		t.Fatalf("decode cursor key: %v", err)
	}
	workspace := identities.Workspaces[0]
	run := &rankingRun{
		driver: datagen.NewDriver(graph, false, seed), token: workspace.Actors[0].Token, workspace: workspace.Slug,
		stores: stores, client: opensearchapi.NewFromClient(base), cursors: tools.NewSearchCursorCodec(key),
		query: "", target: uuid.Nil, names: map[uuid.UUID]string{},
	}
	identifier := json.RawMessage(strconv.Quote(rankingProject))
	run.call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: workspace.Slug, Name: "Ranking regression", Properties: datagen.NodeProperties{"identifier": identifier},
	})
	return run
}

func (r *rankingRun) call(t *testing.T, tool string, arguments datagen.ToolArguments) datagen.Result {
	t.Helper()
	result, err := r.driver.Call(t.Context(), r.token, tool, arguments)
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	return result
}

func (r *rankingRun) createIssue(t *testing.T, name, description string) uuid.UUID {
	t.Helper()
	encoded, err := json.Marshal(description)
	if err != nil {
		t.Fatalf("encode description of %q: %v", name, err)
	}
	result := r.call(t, "tack_create_issue", datagen.ToolArguments{
		WorkspaceReference: r.workspace, ProjectReference: rankingProject, Name: name,
		Properties: datagen.NodeProperties{"description": encoded},
	})
	nodeID, err := uuid.Parse(result.RawID())
	if err != nil {
		t.Fatalf("parse issue ID of %q: %v", name, err)
	}
	r.names[nodeID] = name
	return nodeID
}

func (r *rankingRun) search(t *testing.T, cursor string) rankingPage {
	t.Helper()
	text := r.call(t, "tack_search", datagen.ToolArguments{WorkspaceReference: r.workspace, Query: r.query, Cursor: cursor}).Text()
	page := rankingPage{IDs: []uuid.UUID{}, Cursor: "", Complete: strings.Contains(text, "The search has no more results.")}
	for _, line := range strings.Split(text, "\n") {
		if marker := strings.LastIndex(line, "Raw id: `"); marker >= 0 {
			nodeID, err := uuid.Parse(strings.TrimSuffix(line[marker+len("Raw id: `"):], "`"))
			if err != nil {
				t.Fatalf("parse search result ID in %q: %v", line, err)
			}
			page.IDs = append(page.IDs, nodeID)
		}
		if strings.HasPrefix(line, "Next cursor: `") {
			page.Cursor = strings.TrimSuffix(strings.TrimPrefix(line, "Next cursor: `"), "`")
		}
	}
	if page.Complete == (page.Cursor != "") {
		t.Fatalf("search page must have exactly one of a cursor or completion:\n%s", text)
	}
	return page
}

func (r *rankingRun) require(t *testing.T, phase string) {
	t.Helper()
	for attempt := range 3 {
		page := r.search(t, "")
		rank := slices.Index(page.IDs, r.target) + 1
		t.Logf("regression phase=%s query=%q attempt=%d rank=%d", phase, r.query, attempt+1, rank)
		if attempt == 0 || rank == 0 {
			r.trace(t, page)
		}
		if rank == 0 {
			t.Fatalf("attempt %d of %q did not return %q in the first page", attempt+1, r.query, r.names[r.target])
		}
		r.completeCursor(t, page.Cursor)
	}
}

func (r *rankingRun) completeCursor(t *testing.T, cursor string) {
	t.Helper()
	if cursor == "" {
		return
	}
	tokens := bytes.Clone(r.session(t, cursor).Snapshot.QueryTokens)
	for range rankingMaxPages {
		page := r.search(t, cursor)
		if !bytes.Equal(tokens, r.session(t, cursor).Snapshot.QueryTokens) {
			t.Fatal("continuation changed the saved query tokens")
		}
		replay := r.search(t, cursor)
		if !slices.Equal(page.IDs, replay.IDs) || page.Cursor != replay.Cursor || page.Complete != replay.Complete {
			t.Fatal("replaying a cursor changed its page")
		}
		if page.Complete {
			t.Logf("regression query=%q continuation_complete=true token_bytes=%d", r.query, len(tokens))
			return
		}
		cursor = page.Cursor
	}
	t.Fatalf("continuation did not complete within %d pages", rankingMaxPages)
}

func (r *rankingRun) session(t *testing.T, cursor string) searchdomain.Session {
	t.Helper()
	sessionID, _, err := r.cursors.Decode(cursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	session, err := r.stores.SearchSessions(clock.Wall{}, 15*time.Minute).Load(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", sessionID, err)
	}
	return session
}
