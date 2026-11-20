package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func TestNormalizeCreatePropsRejectsUnknownProperty(t *testing.T) {
	orgID := uuid.New()
	issueType := &node.NodeType{TypeKey: "issue", Slug: "issue"}
	resolver := &Resolver{typeIndex: map[string]*node.NodeType{"issue": issueType}}
	_, _, _, err := normalizeCreateProps(
		context.Background(),
		NodeTypeBinding{
			PropertyDefs: &fakePropertyDefs{
				defs: []*node.PropertyDef{{Name: "priority", Type: node.PropertyTypeText}},
			},
			Resolver: resolver,
		},
		issueType,
		orgID,
		uuid.New(),
		map[string]json.RawMessage{"made_up_field": mustRaw(t, "x")},
	)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("normalizeCreateProps unknown property err = %v, want ErrInvalidArgument", err)
	}
}

func TestNormalizeCreatePropsRejectsAliasAndCanonicalTogether(t *testing.T) {
	orgID := uuid.New()
	issueType := &node.NodeType{
		TypeKey:  "issue",
		Slug:     "issue",
		Features: node.Features{node.FeatureHasWorkflowStates},
	}
	resolver := &Resolver{
		typeIndex: map[string]*node.NodeType{
			"issue": issueType,
			"state": {
				TypeKey: "state",
				Slug:    "state",
				Reference: node.ReferenceConfig{
					Strategy: node.ReferenceScopedProperty,
					Property: "name",
				},
			},
		},
	}
	_, _, _, err := normalizeCreateProps(
		context.Background(),
		NodeTypeBinding{
			PropertyDefs: &fakePropertyDefs{
				defs: []*node.PropertyDef{
					{
						Name:                   "state_id",
						Type:                   node.PropertyTypeUUID,
						AppliesToFeatures:      []string{node.FeatureHasWorkflowStates},
						ReferenceTargetTypeKey: "state",
					},
				},
			},
			Resolver: resolver,
		},
		issueType,
		orgID,
		uuid.New(),
		map[string]json.RawMessage{
			"state":    mustRaw(t, "Done"),
			"state_id": mustRaw(t, "Done"),
		},
	)
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("normalizeCreateProps ambiguous err = %v, want ErrInvalidArgument", err)
	}
}
