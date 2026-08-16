package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

const (
	referenceShapeOrgType       = "org"
	referenceShapeWorkspaceType = "workspace"
	referenceShapeProjectType   = "project"
	referenceShapeIssueType     = "issue"
	referenceShapeLogEvery      = 250
)

// referenceShapeNode is one node the shape writes, in the flat form every
// level of the corpus shares.
type referenceShapeNode struct {
	ID       uuid.UUID
	OrgID    uuid.UUID
	TypeKey  string
	Name     string
	Props    map[string]json.RawMessage
	Indexed  []string
	ParentID uuid.UUID
}

// writeReferenceShape puts the corpus in place and returns how many nodes it
// created. Re-running skips nodes that are already there, so an interrupted
// run resumes instead of writing the corpus twice.
func writeReferenceShape(ctx context.Context, env *Env, shape referenceShape) (int, error) {
	created, err := ensureReferenceShapeNode(ctx, env.Stores, referenceShapeNode{
		ID: shape.OrgID, OrgID: shape.OrgID, TypeKey: referenceShapeOrgType,
		Name:    "Goodkind",
		Props:   referenceShapeProps(map[string]string{"slug": productionSeedOrgSlug}),
		Indexed: []string{"slug"}, ParentID: uuid.Nil,
	})
	if err != nil {
		return 0, err
	}
	if err := service.NewSeeder(env.Stores.PropertyDefs, env.Stores.NodeTypes).
		SeedOrg(ctx, shape.OrgID); err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.seed_org_failed",
			slog.String("err", err.Error()))
		return created, fmt.Errorf("seed org definitions for the reference shape: %w", err)
	}
	written, err := writeReferenceShapeScopes(ctx, env, shape)
	created += written
	if err != nil {
		return created, err
	}
	written, err = writeReferenceShapeIssues(ctx, env, shape)
	created += written
	return created, err
}

func writeReferenceShapeScopes(ctx context.Context, env *Env, shape referenceShape) (int, error) {
	created, err := ensureReferenceShapeNode(ctx, env.Stores, referenceShapeNode{
		ID: shape.WorkspaceID, OrgID: shape.OrgID, TypeKey: referenceShapeWorkspaceType,
		Name: "Main",
		Props: referenceShapeProps(map[string]string{
			"slug": productionSeedWorkspaceSlug, "parent_id": shape.OrgID.String(),
		}),
		Indexed: []string{"slug", "parent_id"}, ParentID: shape.OrgID,
	})
	if err != nil {
		return created, err
	}
	for _, project := range shape.Projects {
		written, projectErr := ensureReferenceShapeNode(ctx, env.Stores, referenceShapeNode{
			ID: project.ID, OrgID: shape.OrgID, TypeKey: referenceShapeProjectType,
			Name: project.Identifier,
			Props: referenceShapeProps(map[string]string{
				"identifier": project.Identifier,
				"slug":       strings.ToLower(project.Identifier),
				"parent_id":  shape.WorkspaceID.String(),
			}),
			Indexed: []string{"identifier", "slug", "parent_id"}, ParentID: shape.WorkspaceID,
		})
		created += written
		if projectErr != nil {
			return created, projectErr
		}
	}
	return created, nil
}

func writeReferenceShapeIssues(ctx context.Context, env *Env, shape referenceShape) (int, error) {
	scopes := make(map[string]uuid.UUID, len(shape.Projects))
	for _, project := range shape.Projects {
		scopes[project.Identifier] = project.ID
	}
	created := 0
	for index, issue := range shape.Issues {
		scopeID := scopes[issue.Project]
		props := referenceShapeProps(map[string]string{
			"parent_id": scopeID.String(), "scope_id": scopeID.String(),
		})
		props["sequence"] = json.RawMessage(strconv.Itoa(issue.Sequence))
		written, err := ensureReferenceShapeNode(ctx, env.Stores, referenceShapeNode{
			ID: issue.ID, OrgID: shape.OrgID, TypeKey: referenceShapeIssueType,
			Name:    referenceShapeReference(issue.Project, issue.Sequence),
			Props:   props,
			Indexed: []string{"parent_id", "scope_id", "sequence"}, ParentID: scopeID,
		})
		created += written
		if err != nil {
			return created, err
		}
		if (index+1)%referenceShapeLogEvery == 0 {
			telemetry.L(ctx).InfoContext(ctx, "qa.reference_shape.progress",
				slog.Int("written", index+1), slog.Int("total", len(shape.Issues)))
		}
	}
	return created, nil
}

// ensureReferenceShapeNode writes one node and reports whether it created it.
func ensureReferenceShapeNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	input referenceShapeNode,
) (int, error) {
	existing, err := stores.Views.Get(ctx, input.ID)
	if err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.read_failed",
			slog.String("node_id", input.ID.String()), slog.String("err", err.Error()))
		return 0, fmt.Errorf("read %s %s for the reference shape: %w", input.TypeKey, input.ID, err)
	}
	if existing != nil {
		return 0, nil
	}
	value := &node.Node{
		ID: input.ID, OrgID: input.OrgID, NodeType: input.TypeKey, Name: input.Name,
		Props: input.Props, CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil,
		CreatedAt: referenceShapeEpoch, UpdatedAt: referenceShapeEpoch,
	}
	view := &node.NodeView{
		ID: input.ID, OrgID: input.OrgID, NodeType: input.TypeKey, Name: input.Name,
		Props: input.Props, CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil,
		CreatedAt: referenceShapeEpoch, UpdatedAt: referenceShapeEpoch,
	}
	relationships := make([]*node.Relationship, 0, 1)
	if input.ParentID != uuid.Nil {
		relationships = append(relationships, &node.Relationship{
			OrgID: input.OrgID, SourceID: input.ID, RelationType: node.RelChildOf,
			TargetID: input.ParentID, CreatedBy: uuid.Nil, CreatedAt: referenceShapeEpoch,
			Props: nil,
		})
	}
	if err := stores.Nodes.CreateAtomic(
		ctx, value, view, relationships, input.Indexed, nil, nil,
	); err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.write_failed",
			slog.String("node_id", input.ID.String()), slog.String("err", err.Error()))
		return 0, fmt.Errorf("write %s %s for the reference shape: %w", input.TypeKey, input.ID, err)
	}
	return 1, nil
}

func referenceShapeProps(values map[string]string) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage, len(values)+1)
	for name, value := range values {
		props[name] = json.RawMessage(strconv.Quote(value))
	}
	return props
}
