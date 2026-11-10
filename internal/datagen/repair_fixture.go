package datagen

import (
	"context"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/service"
)

// RepairFixture records the organization, project, epic, project Done state,
// and repair record IDs that SeedRepairFixture creates. It also records the
// organization ID suffix that repair IDs use.
type RepairFixture struct {
	OrgID     uuid.UUID
	Suffix    string
	ProjectID uuid.UUID
	EpicID    uuid.UUID
	DoneState uuid.UUID
	// MultipleEdges is the project issue with an extra child_of edge to the epic.
	MultipleEdges uuid.UUID
	// LegacyStatus is the issue with status Todo and priority display label High.
	LegacyStatus uuid.UUID
	// PrintedParent is the issue under the epic with QAREPAIR-1 in parent_id.
	PrintedParent uuid.UUID
	// AbsentParent is the project issue with an absent parent UUID in parent_id.
	AbsentParent uuid.UUID
	// Notice is the duplicate notice comment without a child_of edge and with
	// an absent parent UUID in parent_id.
	Notice uuid.UUID
	// NoticeTarget is the issue that the repair selects as the parent of Notice.
	NoticeTarget uuid.UUID
	// SmokeComment is the test comment that the repair exports and deletes.
	SmokeComment uuid.UUID
}

// SeedRepairFixture creates a datagen organization with a fresh UUIDv7 ID,
// seeds its default node types and property definitions, and creates its
// graph and legacy records as actorID. It writes only in that organization.
// It returns the IDs it created before the first error together with that
// error.
func SeedRepairFixture(ctx context.Context, stores *fdbadapter.Stores, members org.MemberRepository, actorID uuid.UUID) (RepairFixture, error) {
	orgID := uuid.Must(uuid.NewV7())
	suffix := strings.ReplaceAll(orgID.String(), "-", "")[20:]
	err := ensureBootstrapNode(ctx, stores, bootstrapNode{
		ID: orgID, OrgID: orgID, TypeKey: "org", Name: "QA Repair Organization " + suffix,
		Slug: "qa-repair-" + suffix, ParentID: uuid.Nil,
	})
	if err != nil {
		return RepairFixture{}, err
	}
	if err := service.NewSeeder(stores.PropertyDefs, stores.NodeTypes).SeedOrg(ctx, orgID); err != nil {
		return RepairFixture{}, loggedError(ctx, "qa datagen: seed repair organization "+orgID.String(), err)
	}
	nodes, err := service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships, stores.NodeDeleter, members, stores.Relationships)
	if err != nil {
		return RepairFixture{}, loggedError(ctx, "qa datagen: build the repair node service", err)
	}
	builder := &repairFixtureBuilder{nodes: nodes, stores: stores, orgID: orgID, actorID: actorID, err: nil}
	fixture := builder.build(ctx)
	fixture.OrgID, fixture.Suffix = orgID, suffix
	return fixture, builder.err
}
