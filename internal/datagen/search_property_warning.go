package datagen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// warningSearchOrder places the warning property after the included and excluded properties.
const warningSearchOrder = 2

// warningPhrases stores the warning node name and the valid and invalid text from the fixture JSON.
type warningPhrases struct {
	Name    string `json:"name"`
	Valid   string `json:"valid"`
	Invalid string `json:"invalid"`
}

// SearchWarningCase reports the verified warning for a node value with an inapplicable
// property. The verification deletes the node after it verifies the warning.
type SearchWarningCase struct {
	NodeID        uuid.UUID `json:"node_id"`
	PropertyName  string    `json:"property_name"`
	PropertyDefID uuid.UUID `json:"property_def_id"`
	Reason        string    `json:"reason"`
	Revision      string    `json:"revision"`
}

// verifyPropertyWarning makes a stored property inapplicable and checks that the serving
// index omits its text and FoundationDB lists its warning. It also checks that tack_search
// returns the node for its name and valid property text. It then deletes the node, which
// strict validation rejects while it stores the inapplicable property, and returns the
// warning case. When a check fails, it still deletes the node and returns both errors.
func (r searchRun) verifyPropertyWarning(ctx context.Context) (SearchWarningCase, error) {
	phrases := r.phrases.Warning
	definition := searchDefinition(r.fixture.orgID, opaqueKey("p"), true, warningSearchOrder)
	if err := r.fixture.stores.PropertyDefs.Set(ctx, definition); err != nil {
		return SearchWarningCase{}, loggedError(ctx, "qa datagen: store warning property "+definition.Name, err)
	}
	nodeID, err := r.fixture.putProps(ctx, phrases.Name, map[string]json.RawMessage{
		r.fixture.includedKey: json.RawMessage(strconv.Quote(phrases.Valid)),
		definition.Name:       json.RawMessage(strconv.Quote(phrases.Invalid)),
	})
	if err != nil {
		return SearchWarningCase{}, err
	}
	warningCase, err := r.observeWarning(ctx, nodeID, definition)
	if err != nil {
		joined := errors.Join(err, r.fixture.remove(ctx, nodeID))
		return SearchWarningCase{}, loggedError(ctx, "qa datagen: verify the warning of node "+nodeID.String(), joined)
	}
	if err := r.removeWarningNode(ctx, nodeID); err != nil {
		return SearchWarningCase{}, err
	}
	return warningCase, nil
}

// observeWarning restricts the definition to a feature that the node type lacks and checks
// the serving index, the warning listing, and the node's remaining search text.
func (r searchRun) observeWarning(ctx context.Context, nodeID uuid.UUID, definition *node.PropertyDef) (SearchWarningCase, error) {
	phrases := r.phrases.Warning
	if err := r.eventually(ctx, "find property text before restricting the property to a feature", func(ctx context.Context) error {
		return r.includes(ctx, phrases.Invalid, nodeID)
	}); err != nil {
		return SearchWarningCase{}, err
	}
	narrowed := *definition
	narrowed.AppliesToFeatures = []string{opaqueKey("f")}
	if err := r.fixture.stores.PropertyDefs.Set(ctx, &narrowed); err != nil {
		return SearchWarningCase{}, loggedError(ctx, "qa datagen: restrict applicability of warning property "+definition.Name, err)
	}
	var listed node.PropertyDiagnostic
	if err := r.eventually(ctx, "exclude inapplicable property text from the serving index and list its warning", func(ctx context.Context) error {
		if err := r.notIndexed(ctx, phrases.Invalid, nodeID); err != nil {
			return err
		}
		warning, err := r.listedWarning(ctx, nodeID, definition)
		listed = warning
		return err
	}); err != nil {
		return SearchWarningCase{}, err
	}
	checks := []struct {
		description string
		query       string
	}{
		{"find warning node by name", phrases.Name},
		{"find warning node by its valid property text", phrases.Valid},
	}
	for _, step := range checks {
		if err := r.eventually(ctx, step.description, func(ctx context.Context) error {
			return r.includes(ctx, step.query, nodeID)
		}); err != nil {
			return SearchWarningCase{}, err
		}
	}
	slog.InfoContext(ctx, "qa.datagen.search_warning_verified", slog.String("node_id", nodeID.String()),
		slog.String("property_name", definition.Name), slog.String("reason", string(listed.Reason)))
	return SearchWarningCase{
		NodeID: nodeID, PropertyName: listed.PropertyName, PropertyDefID: listed.PropertyDefID,
		Reason: string(listed.Reason), Revision: listed.Revision,
	}, nil
}

// listedWarning searches the warning listing for the node and property and requires
// inapplicable_property and the expected definition ID. It returns an error if the
// listing does not contain the warning or either value differs from the expected value.
func (r searchRun) listedWarning(ctx context.Context, nodeID uuid.UUID, definition *node.PropertyDef) (node.PropertyDiagnostic, error) {
	cursor := ""
	for {
		page, err := r.fixture.stores.SearchWarnings(ctx, cursor)
		if err != nil {
			return node.PropertyDiagnostic{}, loggedError(ctx, "qa datagen: list search warnings", err)
		}
		for _, warning := range page.Warnings {
			if warning.NodeID != nodeID || warning.PropertyName != definition.Name {
				continue
			}
			if warning.Reason != node.DiagnosticInapplicable || warning.PropertyDefID != definition.ID {
				return warning, fmt.Errorf("search warning for property %s on node %s has reason %s and definition %s instead of reason %s and definition %s",
					definition.Name, nodeID, warning.Reason, warning.PropertyDefID, node.DiagnosticInapplicable, definition.ID)
			}
			return warning, nil
		}
		if page.Done {
			return node.PropertyDiagnostic{}, fmt.Errorf("search warnings do not list property %s on node %s", definition.Name, nodeID)
		}
		cursor = page.NextCursor
	}
}

// putProps uses the node service to create a node with properties under the
// fixture entry. It records the node's title and type for search result lookup.
func (f searchFixture) putProps(ctx context.Context, name string, props map[string]json.RawMessage) (uuid.UUID, error) {
	result, err := f.nodes.Create(ctx, service.CreateInput{
		ParentID: f.entryID, ScopeID: uuid.Nil, NodeTypeKey: f.typeKey, Name: name, Props: props,
		Relationships: nil, ActorID: uuid.Nil,
		IdempotencyKey: "", IdempotencyFingerprint: "", IdempotencySource: "",
	})
	if err != nil {
		return uuid.Nil, loggedError(ctx, "qa datagen: create search node "+name, err)
	}
	f.titles.Add(SearchItem{Title: name, NodeType: f.typeKey}, result.View.ID)
	return result.View.ID, nil
}
