package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/test/integration"
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
	names     map[uuid.UUID]string
	titles    *datagen.SearchTitles
}

type rankingProbe struct {
	Query   string
	Targets []uuid.UUID
	First   bool
}

type rankingPage struct {
	IDs      []uuid.UUID
	Cursor   string
	Complete bool
}

// newRankingRun creates a ranking run and records the workspace entry node
// in its title registry. Search can return that node, and the workspace type
// does not provide a public get tool.
func newRankingRun(t *testing.T, cfg *config.Config, graph *appruntime.Graph) *rankingRun {
	t.Helper()
	seed := clock.Now().UnixNano()
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(integration.WithTestOperation(t, t.Context()), cfg, seed, scale)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open ranking stores: %v", err)
	}
	certificate, err := config.LoadSearchCA(integration.WithTestOperation(t, t.Context()), cfg.SearchCA)
	if err != nil {
		t.Fatalf("load search CA: %v", err)
	}
	settings := search.Config{
		Endpoint: cfg.SearchEndpoint, CA: certificate, Username: cfg.SearchUsername,
		Password: cfg.SearchPassword, RequestTimeout: 2 * time.Minute, MaxRetries: 1,
	}
	baseAPI, err := opensearchapi.NewClient(opensearchapi.Config{Client: settings.ClientConfig(nil)})
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
		stores: stores, client: baseAPI, cursors: tools.NewSearchCursorCodec(key),
		names: map[uuid.UUID]string{}, titles: datagen.NewSearchTitles(),
	}
	entryID := node.WorkspaceID(workspace.OrgID, workspace.Slug)
	entry, err := stores.Views.Get(integration.WithTestOperation(t, t.Context()), entryID)
	if err != nil || entry == nil {
		t.Fatalf("read the workspace entry node %s: %v", entryID, err)
	}
	run.titles.Add(datagen.SearchItem{Title: workspace.Slug, NodeType: entry.NodeType}, entryID)
	identifier := json.RawMessage(strconv.Quote(rankingProject))
	run.call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: workspace.Slug, Name: "Ranking regression", Properties: datagen.NodeProperties{"identifier": identifier},
	})
	return run
}

func (r *rankingRun) call(t *testing.T, tool string, arguments datagen.ToolArguments) datagen.Result {
	t.Helper()
	result, err := r.driver.Call(integration.WithTestOperation(t, t.Context()), r.token, tool, arguments)
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	return result
}

func (r *rankingRun) createIssue(t *testing.T, name, description string) uuid.UUID {
	t.Helper()
	properties := datagen.NodeProperties{}
	if description != "" {
		encoded, err := json.Marshal(description)
		if err != nil {
			t.Fatalf("encode description of %q: %v", name, err)
		}
		properties["description"] = encoded
	}
	result := r.call(t, "tack_create_issue", datagen.ToolArguments{
		WorkspaceReference: r.workspace, ProjectReference: rankingProject, Name: name, Properties: properties,
	})
	nodeID, err := uuid.Parse(result.RawID())
	if err != nil {
		t.Fatalf("parse issue ID of %q: %v", name, err)
	}
	r.names[nodeID] = name
	r.titles.Add(result.SearchItem(), nodeID)
	return nodeID
}

func (r *rankingRun) search(t *testing.T, query, cursor string) rankingPage {
	t.Helper()
	text := r.call(t, "tack_search", datagen.ToolArguments{WorkspaceReference: r.workspace, Query: query, Cursor: cursor}).Text()
	parsed, err := integration.ParseSearchPage(t.Context(), text, datagen.ReferenceIdentities(r.titles, r.driver, r.token, r.workspace))
	if err != nil {
		t.Fatalf("parse search page: %v", err)
	}
	return rankingPage{IDs: parsed.IDs, Cursor: parsed.Cursor, Complete: parsed.Complete}
}

func (r *rankingRun) require(t *testing.T, phase string, probe rankingProbe) {
	t.Helper()
	for attempt := 1; attempt <= 3; attempt++ {
		page := r.search(t, probe.Query, "")
		ranks := make([]int, 0, len(probe.Targets))
		missed := false
		for _, target := range probe.Targets {
			rank := slices.Index(page.IDs, target) + 1
			ranks = append(ranks, rank)
			missed = missed || rank == 0 || (probe.First && rank != 1)
		}
		t.Logf("ranking phase=%s query=%q attempt=%d ranks=%v", phase, probe.Query, attempt, ranks)
		if attempt == 1 || missed {
			r.trace(t, probe, page)
		}
		if missed {
			t.Errorf("attempt %d of %q returned target ranks %v", attempt, probe.Query, ranks)
		}
		r.completeCursor(t, probe.Query, page.Cursor)
	}
}

func (r *rankingRun) completeCursor(t *testing.T, query, cursor string) {
	t.Helper()
	if cursor == "" {
		return
	}
	tokens := bytes.Clone(r.session(t, cursor).Snapshot.QueryTokens)
	for range rankingMaxPages {
		page := r.search(t, query, cursor)
		if !bytes.Equal(tokens, r.session(t, cursor).Snapshot.QueryTokens) {
			t.Fatal("continuation changed the saved query tokens")
		}
		replay := r.search(t, query, cursor)
		if !slices.Equal(page.IDs, replay.IDs) || page.Cursor != replay.Cursor || page.Complete != replay.Complete {
			t.Fatal("replaying a cursor changed its page")
		}
		if page.Complete {
			t.Logf("ranking query=%q continuation_complete=true token_bytes=%d", query, len(tokens))
			return
		}
		cursor = page.Cursor
	}
	t.Fatalf("continuation did not complete within %d pages", rankingMaxPages)
}
