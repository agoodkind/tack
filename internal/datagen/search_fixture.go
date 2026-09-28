package datagen

import (
	"context"
	_ "embed"
	"encoding/json"
	"maps"
	"strconv"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
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

// newSearchFixture stores one included and one excluded definition and one
// node type that lives under the workspace entry node type.
func newSearchFixture(ctx context.Context, stores *fdbadapter.Stores, workspace WorkspaceIdentity) (searchFixture, error) {
	entryID := node.WorkspaceID(workspace.OrgID, workspace.Slug)
	entry, err := stores.Views.Get(ctx, entryID)
	if err != nil || entry == nil {
		return searchFixture{}, loggedError(ctx, "qa datagen: read search entry node "+entryID.String(), errMissing(err))
	}
	fixture := searchFixture{
		stores: stores, orgID: workspace.OrgID, entryID: entryID,
		typeKey: opaqueKey("n"), includedKey: opaqueKey("p"), excludedKey: opaqueKey("p"),
	}
	included := searchDefinition(workspace.OrgID, fixture.includedKey, true, 0)
	excluded := searchDefinition(workspace.OrgID, fixture.excludedKey, false, 1)
	for _, definition := range []*node.PropertyDef{included, excluded} {
		if err := stores.PropertyDefs.Set(ctx, definition); err != nil {
			return searchFixture{}, loggedError(ctx, "qa datagen: store search property "+definition.Name, err)
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
	return fixture, nil
}

func searchDefinition(orgID uuid.UUID, name string, include bool, order int) *node.PropertyDef {
	return &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Type: node.PropertyType(opaqueKey("t")),
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
	nodeID := uuid.Must(uuid.NewV7())
	props := map[string]json.RawMessage{
		f.includedKey: json.RawMessage(strconv.Quote(included)),
		f.excludedKey: json.RawMessage(strconv.Quote(excluded)),
	}
	now := clock.Now().UTC()
	value := &node.Node{
		ID: nodeID, OrgID: f.orgID, NodeType: f.typeKey, Name: name, Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeView{
		ID: nodeID, OrgID: f.orgID, NodeType: f.typeKey, Name: name, Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	parent := &node.Relationship{
		OrgID: f.orgID, SourceID: nodeID, RelationType: node.RelChildOf, TargetID: f.entryID,
		CreatedBy: uuid.Nil, Props: nil, CreatedAt: now,
	}
	if err := f.stores.Nodes.CreateAtomic(ctx, value, view, []*node.Relationship{parent}, nil, nil, nil); err != nil {
		return uuid.Nil, loggedError(ctx, "qa datagen: create search node "+name, err)
	}
	return nodeID, nil
}

// edit replaces the included value of one node.
func (f searchFixture) edit(ctx context.Context, nodeID uuid.UUID, included string) error {
	current, err := f.stores.Nodes.Get(ctx, f.orgID, nodeID)
	if err != nil || current == nil {
		return loggedError(ctx, "qa datagen: read search node "+nodeID.String(), errMissing(err))
	}
	props := maps.Clone(current.Props)
	props[f.includedKey] = json.RawMessage(strconv.Quote(included))
	current.Props, current.UpdatedAt = props, clock.Now().UTC()
	view := &node.NodeView{
		ID: current.ID, OrgID: current.OrgID, NodeType: current.NodeType, Name: current.Name, Props: props,
		CreatedBy: current.CreatedBy, UpdatedBy: current.UpdatedBy, CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt,
	}
	if err := f.stores.Nodes.Set(ctx, current, view); err != nil {
		return loggedError(ctx, "qa datagen: edit search node "+nodeID.String(), err)
	}
	return nil
}

// remove deletes one node.
func (f searchFixture) remove(ctx context.Context, nodeID uuid.UUID) error {
	if err := f.stores.Nodes.Delete(ctx, f.orgID, nodeID); err != nil {
		return loggedError(ctx, "qa datagen: delete search node "+nodeID.String(), err)
	}
	return nil
}
