package integration

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
)

// TestPropertyDefStoreRefusesNewDefinitionWithoutSearchDecision writes a new
// definition without a search declaration through the FoundationDB store.
// The store must return an error with the definition's name and must store
// nothing. A nil declaration on an existing record stays accepted, because
// the missing-declaration repair reads and fixes that state.
func TestPropertyDefStoreRefusesNewDefinitionWithoutSearchDecision(t *testing.T) {
	env := SetupTestEnv(t)
	undeclared := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, Name: opaqueSearchKey("p"), Type: node.PropertyType(opaqueSearchKey("t")),
	}
	err := env.Stores.PropertyDefs.Set(env.Ctx, undeclared)
	if !errors.Is(err, node.ErrInvalidSearchProjection) || !strings.Contains(err.Error(), undeclared.Name) {
		t.Fatalf("store a new definition without a search decision: error = %v, want a refusal with the name %s", err, undeclared.Name)
	}
	stored, err := env.Stores.PropertyDefs.Get(env.Ctx, env.OrgID, undeclared.ID)
	if err != nil || stored != nil {
		t.Fatalf("read the refused definition: %+v, %v, want nothing stored", stored, err)
	}
	for _, definition := range listPropertyDefRecords(t, env) {
		if definition.Name == undeclared.Name {
			t.Fatalf("the property definition list contains the refused definition %s", undeclared.Name)
		}
	}

	existing := *listPropertyDefRecords(t, env)[0]
	existing.Search = nil
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &existing); err != nil {
		t.Fatalf("clear the declaration of the existing definition %s: %v", existing.Name, err)
	}
	cleared, err := env.Stores.PropertyDefs.Get(env.Ctx, env.OrgID, existing.ID)
	if err != nil || cleared == nil || cleared.Search != nil {
		t.Fatalf("read the cleared existing definition: %+v, %v, want a stored record without a declaration", cleared, err)
	}
}

// TestSeededAndGeneratedDefinitionsDeclareSearch runs the QA identity
// bootstrap, which writes the built-in seed and the QA-generated definitions
// of every organization through the production stores. Every definition read
// back from FoundationDB must declare search inclusion or exclusion.
func TestSeededAndGeneratedDefinitionsDeclareSearch(t *testing.T) {
	stores := newSearchStore(t)
	cfg := harnessConfig(t)
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	identities, err := datagen.BootstrapIdentities(t.Context(), cfg, nextHarnessSeed(), scale)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	var orgIDs []uuid.UUID
	for _, workspace := range identities.Workspaces {
		if !slices.Contains(orgIDs, workspace.OrgID) {
			orgIDs = append(orgIDs, workspace.OrgID)
		}
	}
	if len(orgIDs) == 0 {
		t.Fatal("the bootstrap created no organization")
	}
	for _, orgID := range orgIDs {
		definitions, err := stores.PropertyDefs.List(t.Context(), orgID)
		if err != nil {
			t.Fatalf("list the definitions of organization %s: %v", orgID, err)
		}
		if len(definitions) == 0 {
			t.Fatalf("organization %s has no property definitions", orgID)
		}
		var undeclared []string
		for _, definition := range definitions {
			if definition.Search == nil {
				undeclared = append(undeclared, definition.Name)
			}
		}
		if len(undeclared) > 0 {
			t.Fatalf("organization %s stores definitions without a search decision: %v", orgID, undeclared)
		}
		t.Logf("organization %s: all %d definitions declare search", orgID, len(definitions))
	}
}
