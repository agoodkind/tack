package foundationdb_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

// storeLegacyRelationListPeer writes legacy edges directly because current
// relationship validation can refuse inconsistent organization records.
func storeLegacyRelationListPeer(
	t *testing.T,
	database fdb.Database,
	queryOrgID uuid.UUID,
	anchorID uuid.UUID,
	peerID uuid.UUID,
	resolved node.NodeResolve,
	view node.NodeView,
) {
	t.Helper()
	resolveBytes, err := json.Marshal(resolved)
	if err != nil {
		t.Fatalf("encode peer %s resolution: %v", peerID, err)
	}

	viewBytes, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("encode peer %s view: %v", peerID, err)
	}

	transaction, err := database.CreateTransaction()
	if err != nil {
		t.Fatalf("create legacy peer %s transaction: %v", peerID, err)
	}
	defer transaction.Cancel()
	transaction.Set(
		relationListFixtureKey(tuple.Tuple{
			"node_resolve",
			peerID.String(),
		}),
		resolveBytes,
	)
	transaction.Set(
		relationListFixtureKey(tuple.Tuple{
			"node_view",
			resolved.OrgID.String(),
			resolved.NodeType,
			peerID.String(),
		}),
		viewBytes,
	)

	for _, pair := range [][2]uuid.UUID{{anchorID, peerID}, {peerID, anchorID}} {
		edge := node.Relationship{
			OrgID:        queryOrgID,
			SourceID:     pair[0],
			RelationType: node.RelChildOf,
			TargetID:     pair[1],
			CreatedBy:    anchorID,
			CreatedAt:    view.CreatedAt,
			Props:        nil,
		}
		encoded, err := json.Marshal(edge)
		if err != nil {
			t.Fatalf("encode legacy relationship of peer %s: %v", peerID, err)
		}

		transaction.Set(
			relationListFixtureKey(tuple.Tuple{
				"relationship",
				queryOrgID.String(),
				pair[0].String(),
				node.RelChildOf,
				pair[1].String(),
			}),
			encoded,
		)
		transaction.Set(
			relationListFixtureKey(tuple.Tuple{
				"relationship_reverse",
				queryOrgID.String(),
				pair[1].String(),
				node.RelChildOf,
				pair[0].String(),
			}),
			[]byte{},
		)
	}

	if err := transaction.Commit().Get(); err != nil {
		t.Fatalf("commit legacy peer %s: %v", peerID, err)
	}
}

func relationListFixtureKey(values tuple.Tuple) fdb.Key {
	return fdb.Key(append(append([]byte{}, fdbadapter.TestPrefixRange()...), values.Pack()...))
}

func requireRelationPeerIDs(t *testing.T, views []*node.NodeView, want []uuid.UUID) {
	t.Helper()

	actual := make([]uuid.UUID, 0, len(views))
	for _, view := range views {
		actual = append(actual, view.ID)
	}

	if len(actual) != len(want) {
		t.Fatalf("listed peer IDs = %v, want %v", actual, want)
	}

	for _, expectedID := range want {
		if !slices.Contains(actual, expectedID) {
			t.Fatalf("listed peer IDs = %v, want %v", actual, want)
		}
	}
}

func requireRelationPeerPages(
	t *testing.T,
	reader *fdbadapter.ViewStore,
	query node.NodeListQuery,
	want []uuid.UUID,
) {
	t.Helper()
	query.Limit = 1
	var views []*node.NodeView

	for range len(want) + 1 {
		page, err := reader.ListPage(t.Context(), query)
		if err != nil {
			t.Fatalf("list relationship peer page: %v", err)
		}

		views = append(views, page.Views...)
		if page.NextCursor == "" {
			requireRelationPeerIDs(t, views, want)

			return
		}

		query.Cursor = page.NextCursor
	}

	t.Fatalf("relationship peer pagination did not finish within %d pages", len(want)+1)
}
