package foundationdb_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

func TestRelationshipListPageContinuesAcrossFilteredBatches(t *testing.T) {
	database, err := fdbadapter.Open(testenv.FoundationDB(t), strictTestTransactionTimeout)
	if err != nil {
		t.Fatalf("open foundationdb: %v", err)
	}

	prefixID := uuid.Must(uuid.NewV7())
	prefix := append([]byte("tack-test:relation-page:"), prefixID[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() {
		clearStrictTestPrefix(t, database, prefix)
		fdbadapter.SetTestPrefix(nil)
	})
	ctx := relationPageContext(t)
	orgID, parentID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	nodes := fdbadapter.NewNodeStore(database, clock.Wall{})
	relationships := fdbadapter.NewRelationshipStore(database, clock.Wall{})
	want := []uuid.UUID{}
	const candidateCount = 270
	const lastCandidate = candidateCount - 1
	const otherTypeCandidate = 150
	const otherOrgCandidate = 160
	const matchingInterval = 100
	now := time.Now().UTC()

	for index := range candidateCount {
		id := uuid.MustParse(fmt.Sprintf("019e0000-0000-7000-8000-%012x", index+1))
		kind, recordOrg := "work", orgID
		matched := index%matchingInterval == 0 || index == lastCandidate
		if matched {
			want = append(want, id)
		}

		if index == otherTypeCandidate {
			kind, matched = "other", true
		}

		if index == otherOrgCandidate {
			recordOrg, matched = uuid.Must(uuid.NewV7()), true
		}

		props := map[string]json.RawMessage{"selected": json.RawMessage("false")}
		if matched {
			props["selected"] = json.RawMessage("true")
		}

		current, view := relationPageCandidate(id, recordOrg, parentID, kind, props, now)
		if err := nodes.Set(ctx, &current, &view); err != nil {
			t.Fatalf("set candidate %d: %v", index, err)
		}

		for _, edge := range []node.Relationship{
			{
				OrgID:        orgID,
				SourceID:     id,
				RelationType: node.RelChildOf,
				TargetID:     parentID,
				CreatedBy:    parentID,
				CreatedAt:    now,
				Props:        nil,
			},
			{
				OrgID:        orgID,
				SourceID:     parentID,
				RelationType: "contains",
				TargetID:     id,
				CreatedBy:    parentID,
				CreatedAt:    now,
				Props:        nil,
			},
		} {
			if err := relationships.Add(ctx, &edge); err != nil {
				t.Fatalf("add candidate %d relationship: %v", index, err)
			}
		}
	}

	reader := fdbadapter.NewViewStore(database)

	for _, direction := range []string{"target", "source"} {
		t.Run(
			direction,
			func(t *testing.T) {
				query := node.NodeListQuery{
					OrgID:            orgID,
					NodeType:         "work",
					Limit:            2,
					Cursor:           "",
					ByProperty:       nil,
					BySourceRelation: nil,
					ByTargetRelation: nil,
					CreatedAfter:     nil,
					CreatedBefore:    nil,
					PropFilters: []node.PropertyMatch{
						{PropName: "selected", Value: json.RawMessage("true")},
					},
				}
				if direction == "target" {
					query.ByTargetRelation = &node.RelationScan{
						NodeID:       parentID,
						RelationType: node.RelChildOf,
					}
				} else {
					query.BySourceRelation = &node.RelationScan{
						NodeID:       parentID,
						RelationType: "contains",
					}
				}

				var actual []uuid.UUID

				const expectedPageCount = 2
				for pageIndex := range expectedPageCount {
					page, err := reader.ListPage(t.Context(), query)
					if err != nil {
						t.Fatalf("read page %d: %v", pageIndex, err)
					}

					if len(page.Views) != query.Limit {
						t.Fatalf(
							"page %d length = %d, want %d",
							pageIndex,
							len(page.Views),
							query.Limit,
						)
					}

					for _, view := range page.Views {
						actual = append(actual, view.ID)
					}

					if (page.NextCursor == "") != (pageIndex == expectedPageCount-1) {
						t.Fatalf("page %d cursor = %q", pageIndex, page.NextCursor)
					}

					query.Cursor = page.NextCursor
				}

				if !slices.Equal(actual, want) {
					t.Fatalf("listed node IDs = %v, want %v", actual, want)
				}
			},
		)
	}
}
