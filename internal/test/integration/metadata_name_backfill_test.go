package integration

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/testenv"
)

// TestMetadataNameIndexBackfill stores node types and property definitions
// in two organizations and then clears their name index entries, which is
// the stored shape of metadata written before the indexes existed. Search
// access and search text must fail on that shape. The dry run must report
// every missing entry and write none. The executed backfill must restore
// search access and text, and a rerun must find no missing entry.
func TestMetadataNameIndexBackfill(t *testing.T) {
	stores := newSearchStore(t)
	included := "metadata name index text"
	fixtures := []searchFixture{
		putSearchText(t, stores, included, readerExcludedValue),
		putSearchText(t, stores, included, readerExcludedValue),
	}
	clearKeyFamily(t, "node_type_by_key")
	clearKeyFamily(t, "property_def_by_name")
	requireMetadataLookupFailures(t, stores, fixtures)

	planned, err := ops.RunMetadataNameBackfill(t.Context(), stores.NodeTypes, stores.PropertyDefs, true)
	if err != nil {
		t.Fatalf("dry-run metadata name index backfill: %v", err)
	}
	full := ops.MetadataNameBackfillResult{
		NodeTypes:           ops.NameIndexCount{Scanned: 2, Missing: 2},
		PropertyDefinitions: ops.NameIndexCount{Scanned: 4, Missing: 4},
	}
	if planned != full {
		t.Fatalf("dry-run result = %+v, want %+v", planned, full)
	}
	requireMetadataLookupFailures(t, stores, fixtures)

	applied, err := ops.RunMetadataNameBackfill(t.Context(), stores.NodeTypes, stores.PropertyDefs, false)
	if err != nil {
		t.Fatalf("run metadata name index backfill: %v", err)
	}
	if applied != full {
		t.Fatalf("backfill result = %+v, want %+v", applied, full)
	}
	for _, fixture := range fixtures {
		if _, err := indexFixtureAccess(t, stores, fixture); err != nil {
			t.Fatalf("compile search access for node %s after the backfill: %v", fixture.NodeID, err)
		}
		pages := readSearchPages(t, stores, fixture.NodeID, readerPageBytes)
		if got, want := uniqueSearchText(pages), searchFixtureName+"\n"+included+"\n"; got != want {
			t.Fatalf("search text of node %s = %q, want %q", fixture.NodeID, got, want)
		}
	}

	rerun, err := ops.RunMetadataNameBackfill(t.Context(), stores.NodeTypes, stores.PropertyDefs, false)
	if err != nil {
		t.Fatalf("rerun metadata name index backfill: %v", err)
	}
	unchanged := ops.MetadataNameBackfillResult{
		NodeTypes:           ops.NameIndexCount{Scanned: 2, Missing: 0},
		PropertyDefinitions: ops.NameIndexCount{Scanned: 4, Missing: 0},
	}
	if rerun != unchanged {
		t.Fatalf("rerun result = %+v, want %+v", rerun, unchanged)
	}
}

// requireMetadataLookupFailures requires search access to report the missing
// node type and search text to reject the node's unknown properties.
func requireMetadataLookupFailures(t *testing.T, stores *fdbadapter.Stores, fixtures []searchFixture) {
	t.Helper()
	reader := stores.SearchContent(stores.SearchPolicySet())
	for _, fixture := range fixtures {
		_, err := indexFixtureAccess(t, stores, fixture)
		if want := fmt.Sprintf("node type %q is missing", fixture.TypeKey); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("search access error for node %s = %v, want %q", fixture.NodeID, err, want)
		}
		_, err = reader.Content(t.Context(), searchdomain.ContentRequest{
			NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: readerPageBytes, SearchGeneration: 0,
		})
		if !errors.Is(err, node.ErrInvalidSearchProjection) {
			t.Fatalf("search text error for node %s = %v, want %v", fixture.NodeID, err, node.ErrInvalidSearchProjection)
		}
	}
}

func indexFixtureAccess(t *testing.T, stores *fdbadapter.Stores, fixture searchFixture) (node.SearchAccess, error) {
	t.Helper()
	return stores.SearchPolicySet().Index(t.Context(), searchaccess.IndexAccessRequest{
		Version: searchaccess.StableVersion, OrganizationID: fixture.OrgID, ResourceID: fixture.NodeID, Generation: 1,
	})
}

// clearKeyFamily clears every key of one FoundationDB key family under the
// active test prefix.
func clearKeyFamily(t *testing.T, family string) {
	t.Helper()
	database, err := fdbadapter.Open(testenv.FoundationDB(t), testTransactionTimeout)
	if err != nil {
		t.Fatalf("open database to clear %s: %v", family, err)
	}
	prefix := append([]byte{}, fdbadapter.TestPrefixRange()...)
	keyRange, err := fdb.PrefixRange(append(prefix, tuple.Tuple{family}.Pack()...))
	if err != nil {
		t.Fatalf("create %s range: %v", family, err)
	}
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(keyRange)
		return nil, nil
	}); err != nil {
		t.Fatalf("clear %s: %v", family, err)
	}
}
