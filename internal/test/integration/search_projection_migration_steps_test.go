package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const migrationSlices = 2000

// runIdle runs worker slices until it cannot claim work from any class.
// It fails the test if worker slices keep failing for more than
// migrationIdleWait.
func (env *migrationEnv) runIdle(t *testing.T) {
	t.Helper()
	var failingSince time.Time
	for {
		claimed, err := env.Worker.RunSlice(integration.WithTestOperation(t, t.Context()))
		switch {
		case err != nil && failingSince.IsZero():
			failingSince = clock.Now()
		case err != nil && clock.Since(failingSince) > migrationIdleWait:
			t.Fatalf("search work kept failing for %s: %v", migrationIdleWait, err)
		case err != nil:
			time.Sleep(time.Second)
		case !claimed:
			return
		default:
			failingSince = time.Time{}
		}
	}
}

// pages refreshes the index and returns the requested pages of nodeID in page order.
func (env *migrationEnv) pages(t *testing.T, nodeID uuid.UUID, retired bool) []migrationPage {
	t.Helper()
	if _, err := env.Client.Indices.Refresh(integration.WithTestOperation(t, t.Context()), &opensearchapi.IndicesRefreshReq{Indices: []string{env.Index}}); err != nil {
		t.Fatalf("refresh %s: %v", env.Index, err)
	}
	query := fmt.Sprintf(`{"size":1000,"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":%t}}]}},"sort":[{"page_ordinal":"asc"}]}`, nodeID.String(), retired)
	response, err := env.Client.Search(integration.WithTestOperation(t, t.Context()), &opensearchapi.SearchReq{Indices: []string{env.Index}, BodyReader: strings.NewReader(query)})
	if err != nil {
		t.Fatalf("search pages of node %s: %v", nodeID, err)
	}
	pages := make([]migrationPage, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		var page migrationPage
		if err := json.Unmarshal(hit.Source, &page); err != nil {
			t.Fatalf("decode page of node %s: %v", nodeID, err)
		}
		pages = append(pages, page)
	}
	return pages
}

// revisions returns the revision of the first active page of each node.
func (env *migrationEnv) revisions(t *testing.T, nodes []uuid.UUID) map[uuid.UUID]int64 {
	t.Helper()
	revisions := make(map[uuid.UUID]int64, len(nodes))
	for _, nodeID := range nodes {
		pages := env.pages(t, nodeID, false)
		if len(pages) == 0 {
			t.Fatalf("node %s has no active pages", nodeID)
		}
		revision, err := strconv.ParseInt(pages[0].NodeRevision, 10, 64)
		if err != nil {
			t.Fatalf("parse revision of node %s: %v", nodeID, err)
		}
		revisions[nodeID] = revision
	}
	return revisions
}

// readerPages reads every page of the node's current revision through the
// production reader.
func (env *migrationEnv) readerPages(t *testing.T, nodeID uuid.UUID) []node.ContentPage {
	t.Helper()
	reader := env.Stores.SearchContent(env.Stores.SearchPolicySet())
	request := searchdomain.ContentRequest{NodeID: nodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: env.PageBytes, SearchGeneration: 0}
	pages := make([]node.ContentPage, 0, 8)
	for range migrationSlices {
		page, err := reader.Content(integration.WithTestOperation(t, t.Context()), request)
		if err != nil {
			t.Fatalf("read page %d of node %s: %v", len(pages), nodeID, err)
		}
		pages = append(pages, page)
		if page.Done {
			return pages
		}
		request.Cursor = page.NextCursor
	}
	t.Fatalf("node %s did not finish within %d pages", nodeID, migrationSlices)
	return nil
}

// requireMigrated checks each node's active page count, text, and projection
// against the reader's pages. It requires a later revision than the revision
// in before. Every page must use one projection that differs from previous.
// It returns that projection.
func (env *migrationEnv) requireMigrated(t *testing.T, nodes []uuid.UUID, before map[uuid.UUID]int64, previous string) string {
	t.Helper()
	projection := ""
	for nodeID, revision := range env.revisions(t, nodes) {
		if revision <= before[nodeID] {
			t.Fatalf("node %s kept revision %d, want a revision after %d", nodeID, revision, before[nodeID])
		}
		documents, expected := env.pages(t, nodeID, false), env.readerPages(t, nodeID)
		if len(documents) != len(expected) {
			t.Fatalf("node %s has %d active pages, want %d", nodeID, len(documents), len(expected))
		}
		for number, document := range documents {
			if projection == "" {
				projection = document.ProjectionVersion
			}
			page := expected[number]
			if document.PageText == nil || *document.PageText != page.Text || document.ProjectionVersion != page.ProjectionVersion ||
				document.ProjectionVersion != projection || projection == previous {
				t.Fatalf("node %s page %d does not match the reader under one projection other than %q", nodeID, number, previous)
			}
		}
	}
	return projection
}

// requireRetiredRevision requires at least one retired page of the node
// with the given revision.
func (env *migrationEnv) requireRetiredRevision(t *testing.T, nodeID uuid.UUID, revision int64) {
	t.Helper()
	for _, page := range env.pages(t, nodeID, true) {
		if page.NodeRevision == strconv.FormatInt(revision, 10) {
			return
		}
	}
	t.Fatalf("node %s has no retired page of revision %d", nodeID, revision)
}

// forgetProjectionState removes the organization's projection state record
// and the environment's projection sweep record. This creates a fixture
// that simulates a release without these records. Existing pages retain
// their earlier projection identity. The next sweep starts before the
// first organization.
func (env *migrationEnv) forgetProjectionState(t *testing.T) {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), 5*time.Second)
	if err != nil {
		t.Fatalf("open projection fixture database: %v", err)
	}
	prefix := fdbadapter.TestPrefixRange()
	_, err = database.Transact(func(tr fdb.Transaction) (any, error) {
		for _, key := range []tuple.Tuple{{"search_projection_state", env.OrgID.String()}, {"search_projection_sweep"}} {
			tr.Clear(fdb.Key(append(slices.Clone(prefix), key.Pack()...)))
		}
		return nil, nil
	})
	if err != nil {
		t.Fatalf("remove projection state of organization %s: %v", env.OrgID, err)
	}
}

// driveRescan claims and processes rescan work until stop accepts a claim.
// It yields that work and returns it with the number of slices it processed.
// Before each claim, it calls between with that number.
func (env *migrationEnv) driveRescan(t *testing.T, stop func(searchdomain.Work) bool, between func(int)) (searchdomain.Work, int) {
	t.Helper()
	store := env.Stores.SearchWork(clock.Wall{})
	for processed := range migrationSlices {
		between(processed)
		work, claimed, err := store.Claim(integration.WithTestOperation(t, t.Context()), searchdomain.WorkClassRescan, "migration-driver", time.Minute)
		if err != nil || !claimed {
			t.Fatalf("claim rescan work = claimed %t, err %v", claimed, err)
		}
		if stop(work) {
			if err := store.Yield(integration.WithTestOperation(t, t.Context()), work); err != nil && !errors.Is(err, searchdomain.ErrWorkChanged) {
				t.Fatalf("yield rescan work: %v", err)
			}
			return work, processed
		}
		if err := env.Worker.Process(integration.WithTestOperation(t, t.Context()), work); err != nil {
			t.Fatalf("process rescan work: %v", err)
		}
	}
	t.Fatalf("the rescan did not stop within %d slices", migrationSlices)
	return searchdomain.Work{}, 0
}
