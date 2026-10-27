package datagen

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/service"
)

//go:embed search_phrases.json
var searchPhrasesJSON []byte

// semanticPair is one query and the node text it must rank.
type semanticPair struct {
	Query string `json:"query"`
	Text  string `json:"text"`
}

// searchPhrases is the fixed search fixture text.
type searchPhrases struct {
	Semantic          []semanticPair `json:"semantic"`
	Distractors       []string       `json:"distractors"`
	FinalPage         string         `json:"final_page"`
	Filler            string         `json:"filler"`
	Excluded          string         `json:"excluded"`
	ExcludedVisible   string         `json:"excluded_visible"`
	EditBefore        string         `json:"edit_before"`
	EditAfter         string         `json:"edit_after"`
	Deleted           string         `json:"deleted"`
	Continuation      string         `json:"continuation"`
	CreatedThroughMCP string         `json:"created_through_mcp"`
	Warning           warningPhrases `json:"warning"`
	Relevance         probePhrases   `json:"relevance_probe"`
}

func loadSearchPhrases(ctx context.Context) (searchPhrases, error) {
	var phrases searchPhrases
	if err := json.Unmarshal(searchPhrasesJSON, &phrases); err != nil {
		return searchPhrases{}, loggedError(ctx, "qa datagen: decode search phrases", err)
	}
	return phrases, nil
}

// searchFixture is one generated opaque node type under one entry node. The
// type and both property names are UUID-derived. Search behavior depends
// only on their stored declarations.
type searchFixture struct {
	stores      *fdbadapter.Stores
	members     org.MemberRepository
	nodes       *service.NodeService
	orgID       uuid.UUID
	entryID     uuid.UUID
	typeKey     string
	includedKey string
	excludedKey string
}

// opaqueKey returns a lowercase identifier with a label prefix and no
// product meaning.
func opaqueKey(label string) string {
	return label + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")[20:]
}

// openSearchFixtureStores opens FoundationDB stores with search work enabled
// and the ledger org_members repository. The caller runs the returned close
// function after the last fixture write.
func openSearchFixtureStores(ctx context.Context, cfg *config.Config, purpose string) (*fdbadapter.Stores, org.MemberRepository, func(), error) {
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		return nil, nil, nil, loggedError(ctx, "qa datagen: open foundationdb for "+purpose, err)
	}
	stores.EnableSearchWork()
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		return nil, nil, nil, loggedError(ctx, "qa datagen: open postgres for "+purpose, err)
	}
	return stores, postgres.NewOrgMemberRepo(pool), pool.Close, nil
}

// newSearchFixture stores one included and one excluded definition and one
// node type that lives under the workspace entry node type.
func newSearchFixture(ctx context.Context, stores *fdbadapter.Stores, members org.MemberRepository, workspace WorkspaceIdentity) (searchFixture, error) {
	return newSearchFixtureWithManifest(ctx, stores, members, workspace, nil)
}

func newSearchFixtureWithManifest(
	ctx context.Context, stores *fdbadapter.Stores, members org.MemberRepository, workspace WorkspaceIdentity, manifest *SearchManifest,
) (searchFixture, error) {
	entryID := node.WorkspaceID(workspace.OrgID, workspace.Slug)
	entry, err := stores.Views.Get(ctx, entryID)
	if err != nil || entry == nil {
		return searchFixture{}, loggedError(ctx, "qa datagen: read search entry node "+entryID.String(), errMissing(err))
	}
	nodes, err := service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships, stores.NodeDeleter, members, stores.Relationships)
	if err != nil {
		return searchFixture{}, loggedError(ctx, "qa datagen: build the search fixture node service", err)
	}
	fixture := searchFixture{
		stores: stores, members: members, nodes: nodes, orgID: workspace.OrgID, entryID: entryID,
		typeKey: opaqueKey("n"), includedKey: opaqueKey("p"), excludedKey: opaqueKey("p"),
	}
	included := searchDefinition(workspace.OrgID, fixture.includedKey, true, 0)
	excluded := searchDefinition(workspace.OrgID, fixture.excludedKey, false, 1)
	for _, definition := range []*node.PropertyDef{included, excluded} {
		if err := stores.PropertyDefs.Set(ctx, definition); err != nil {
			return searchFixture{}, loggedError(ctx, "qa datagen: store search property "+definition.Name, err)
		}
		if manifest != nil {
			manifest.Properties = append(manifest.Properties, SearchManifestProperty{
				ID: definition.ID, Key: definition.Name, Included: definition.Search.Include,
			})
		}
	}
	var reference node.ReferenceConfig
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: workspace.OrgID, Name: fixture.typeKey, Slug: fixture.typeKey,
		Color: "", Icon: "", AllowedOps: nil, PropertyDefIDs: []uuid.UUID{included.ID, excluded.ID},
		PluralSlug: fixture.typeKey + "s", IsBuiltin: false, TypeKey: fixture.typeKey, Features: nil,
		CanContain: nil, CanLiveUnder: []string{entry.NodeType}, Reference: reference,
		ReferenceTemplates: nil, DefaultChildren: nil,
	}
	if err := stores.NodeTypes.Set(ctx, kind); err != nil {
		return searchFixture{}, loggedError(ctx, "qa datagen: store search node type "+fixture.typeKey, err)
	}
	if manifest != nil {
		manifest.Types = append(manifest.Types, SearchManifestType{ID: kind.ID, Key: kind.TypeKey})
	}
	return fixture, nil
}

func searchDefinition(orgID uuid.UUID, name string, include bool, order int) *node.PropertyDef {
	return &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Type: node.PropertyTypeText,
		AppliesToFeatures: nil, Indexed: false, Options: nil, Required: false, DefaultValue: nil,
		DefaultReference: nil, ReferenceTargetTypeKey: "",
		Search: &node.SearchProjection{Include: include, Order: order, Rule: node.TextRule{
			Mode: node.TextRuleScalar, Fields: nil, Items: nil, Labels: nil,
		}},
	}
}

// put creates one node under the entry node through the production write
// path. That write schedules the node's search work in the same transaction.
func (f searchFixture) put(ctx context.Context, name, included, excluded string) (uuid.UUID, error) {
	props := map[string]json.RawMessage{
		f.includedKey: json.RawMessage(strconv.Quote(included)),
		f.excludedKey: json.RawMessage(strconv.Quote(excluded)),
	}
	result, err := f.nodes.Create(ctx, service.CreateInput{
		ParentID: f.entryID, ScopeID: uuid.Nil, NodeTypeKey: f.typeKey, Name: name, Props: props,
		Relationships: nil, ActorID: uuid.Nil,
		IdempotencyKey: "", IdempotencyFingerprint: "", IdempotencySource: "",
	})
	if err != nil {
		return uuid.Nil, loggedError(ctx, "qa datagen: create search node "+name, err)
	}
	return result.View.ID, nil
}

// edit replaces the included value of one node.
func (f searchFixture) edit(ctx context.Context, nodeID uuid.UUID, included string) error {
	if _, err := f.nodes.Update(ctx, service.UpdateInput{
		NodeID: nodeID, Name: nil, Props: map[string]json.RawMessage{f.includedKey: json.RawMessage(strconv.Quote(included))},
		RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil}, ActorID: uuid.Nil,
	}); err != nil {
		return loggedError(ctx, "qa datagen: edit search node "+nodeID.String(), err)
	}
	return nil
}

// errDeleteUnfinished reports that the delete job did not delete the node
// within the request budget.
var errDeleteUnfinished = errors.New("delete job did not delete the node within the request budget")

// remove deletes one node.
func (f searchFixture) remove(ctx context.Context, nodeID uuid.UUID) error {
	result, err := f.nodes.Delete(ctx, nodeID, uuid.Nil)
	if err != nil {
		return loggedError(ctx, "qa datagen: delete search node "+nodeID.String(), err)
	}
	if result.State != node.SubtreeDeleteFinished {
		return loggedError(ctx, "qa datagen: delete search node "+nodeID.String(), errDeleteUnfinished)
	}
	return nil
}
