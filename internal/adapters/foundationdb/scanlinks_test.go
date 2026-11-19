package foundationdb_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

func TestRelationshipListExcludesForeignPeers(t *testing.T) {
	database, err := fdbadapter.Open(testenv.FoundationDB(t), strictTestTransactionTimeout)
	if err != nil {
		t.Fatalf("open foundationdb: %v", err)
	}

	prefixID := uuid.Must(uuid.NewV7())
	prefix := append([]byte("tack-test:relation-list:"), prefixID[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() {
		clearStrictTestPrefix(t, database, prefix)
		fdbadapter.SetTestPrefix(nil)
	})
	orgID, foreignOrgID, anchorID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(
		uuid.NewV7(),
	)
	wantWork := []uuid.UUID{}
	wantAll := []uuid.UUID{}

	for _, candidate := range []string{
		"local",
		"foreign",
		"other-type",
		"view-org",
		"view-type",
		"view-id",
	} {
		peerID := uuid.Must(uuid.NewV7())
		resolved := node.NodeResolve{OrgID: orgID, NodeType: "work"}
		if candidate == "foreign" {
			resolved.OrgID = foreignOrgID
		}

		if candidate == "other-type" {
			resolved.NodeType = "other"
		}

		now := time.Now().UTC()
		view := node.NodeView{
			ID:        peerID,
			OrgID:     resolved.OrgID,
			NodeType:  resolved.NodeType,
			Name:      candidate,
			Props:     nil,
			CreatedBy: anchorID,
			UpdatedBy: anchorID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		switch candidate {
		case "local":
			wantWork = append(wantWork, peerID)
			wantAll = append(wantAll, peerID)
		case "other-type":
			wantAll = append(wantAll, peerID)
		case "view-org":
			view.OrgID = foreignOrgID
		case "view-type":
			view.NodeType = "other"
		case "view-id":
			view.ID = uuid.Must(uuid.NewV7())
		}

		storeLegacyRelationListPeer(t, database, orgID, anchorID, peerID, resolved, view)
	}

	reader := fdbadapter.NewViewStore(database)

	for _, direction := range []string{"source", "target"} {
		t.Run(
			direction,
			func(t *testing.T) {
				for _, nodeType := range []string{"work", ""} {
					t.Run(
						"type="+nodeType,
						func(t *testing.T) {
							query := node.NodeListQuery{
								OrgID:            orgID,
								NodeType:         nodeType,
								Limit:            0,
								Cursor:           "",
								ByProperty:       nil,
								BySourceRelation: nil,
								ByTargetRelation: nil,
								CreatedAfter:     nil,
								CreatedBefore:    nil,
								PropFilters:      nil,
							}
							scan := &node.RelationScan{
								NodeID:       anchorID,
								RelationType: node.RelChildOf,
							}
							if direction == "source" {
								query.BySourceRelation = scan
							} else {
								query.ByTargetRelation = scan
							}

							views, err := reader.List(t.Context(), query)
							if err != nil {
								t.Fatalf("list relationship peers: %v", err)
							}

							want := wantWork
							if nodeType == "" {
								want = wantAll
							}

							requireRelationPeerIDs(t, views, want)
							requireRelationPeerPages(t, reader, query, want)
						},
					)
				}
			},
		)
	}
}
