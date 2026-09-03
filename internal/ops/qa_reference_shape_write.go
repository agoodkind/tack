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

// referenceShapeWritten counts what a write did: nodes that did not exist and
// were created, and nodes a repair had moved that were put back on the shape.
type referenceShapeWritten struct {
	Created  int
	Restored int
}

func (w referenceShapeWritten) add(other referenceShapeWritten) referenceShapeWritten {
	return referenceShapeWritten{
		Created:  w.Created + other.Created,
		Restored: w.Restored + other.Restored,
	}
}

// writeReferenceShape puts the corpus in place and returns what it created and
// what it restored. Re-running leaves matching nodes alone, so an interrupted
// run resumes instead of writing the corpus twice, and puts back any node a
// repair moved off the shape, so a repaired org becomes the pre-repair corpus
// again rather than staying repaired under a report that says otherwise.
func writeReferenceShape(ctx context.Context, env *Env, shape referenceShape) (referenceShapeWritten, error) {
	written, err := ensureReferenceShapeNode(ctx, env.Stores, referenceShapeNode{
		ID: shape.OrgID, OrgID: shape.OrgID, TypeKey: referenceShapeOrgType,
		Name:    "Goodkind",
		Props:   referenceShapeProps(map[string]string{"slug": productionSeedOrgSlug}),
		Indexed: []string{"slug"}, ParentID: uuid.Nil,
	})
	if err != nil {
		return written, err
	}
	if err := service.NewSeeder(env.Stores.PropertyDefs, env.Stores.NodeTypes).
		SeedOrg(ctx, shape.OrgID); err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.seed_org_failed",
			slog.String("err", err.Error()))
		return written, fmt.Errorf("seed org definitions for the reference shape: %w", err)
	}
	scopes, err := writeReferenceShapeScopes(ctx, env, shape)
	written = written.add(scopes)
	if err != nil {
		return written, err
	}
	issues, err := writeReferenceShapeIssues(ctx, env, shape)
	return written.add(issues), err
}

func writeReferenceShapeScopes(ctx context.Context, env *Env, shape referenceShape) (referenceShapeWritten, error) {
	written, err := ensureReferenceShapeNode(ctx, env.Stores, referenceShapeNode{
		ID: shape.WorkspaceID, OrgID: shape.OrgID, TypeKey: referenceShapeWorkspaceType,
		Name: "Main",
		Props: referenceShapeProps(map[string]string{
			"slug": productionSeedWorkspaceSlug, "parent_id": shape.OrgID.String(),
		}),
		Indexed: []string{"slug", "parent_id"}, ParentID: shape.OrgID,
	})
	if err != nil {
		return written, err
	}
	for _, project := range shape.Projects {
		projectWritten, projectErr := ensureReferenceShapeNode(ctx, env.Stores, referenceShapeNode{
			ID: project.ID, OrgID: shape.OrgID, TypeKey: referenceShapeProjectType,
			Name: project.Identifier,
			Props: referenceShapeProps(map[string]string{
				"identifier": project.Identifier,
				"slug":       strings.ToLower(project.Identifier),
				"parent_id":  shape.WorkspaceID.String(),
			}),
			Indexed: []string{"identifier", "slug", "parent_id"}, ParentID: shape.WorkspaceID,
		})
		written = written.add(projectWritten)
		if projectErr != nil {
			return written, projectErr
		}
	}
	return written, nil
}

func writeReferenceShapeIssues(ctx context.Context, env *Env, shape referenceShape) (referenceShapeWritten, error) {
	scopes := make(map[string]uuid.UUID, len(shape.Projects))
	for _, project := range shape.Projects {
		scopes[project.Identifier] = project.ID
	}
	var written referenceShapeWritten
	for index, issue := range shape.Issues {
		scopeID := scopes[issue.Project]
		props := referenceShapeProps(map[string]string{
			"parent_id": scopeID.String(), "scope_id": scopeID.String(),
		})
		props[referenceShapeSequenceProp] = json.RawMessage(strconv.Itoa(issue.Sequence))
		issueWritten, err := ensureReferenceShapeNode(ctx, env.Stores, referenceShapeNode{
			ID: issue.ID, OrgID: shape.OrgID, TypeKey: referenceShapeIssueType,
			Name:    referenceShapeReference(issue.Project, issue.Sequence),
			Props:   props,
			Indexed: []string{"parent_id", "scope_id", referenceShapeSequenceProp}, ParentID: scopeID,
		})
		written = written.add(issueWritten)
		if err != nil {
			return written, err
		}
		if (index+1)%referenceShapeLogEvery == 0 {
			telemetry.L(ctx).InfoContext(ctx, "qa.reference_shape.progress",
				slog.Int("written", index+1), slog.Int("total", len(shape.Issues)))
		}
	}
	return written, nil
}

// ensureReferenceShapeNode writes one node and reports what it did to it: a
// missing node is created, and an existing one is restored when a repair has
// moved it off the shape.
func ensureReferenceShapeNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	input referenceShapeNode,
) (referenceShapeWritten, error) {
	existing, err := stores.Views.Get(ctx, input.ID)
	if err != nil {
		slog.ErrorContext(ctx, "qa.reference_shape.read_failed",
			slog.String("node_id", input.ID.String()), slog.String("err", err.Error()))
		return referenceShapeWritten{}, fmt.Errorf("read %s %s for the reference shape: %w", input.TypeKey, input.ID, err)
	}
	if existing != nil {
		restored, restoreErr := restoreReferenceShapeNode(ctx, stores, existing, input)
		return referenceShapeWritten{Created: 0, Restored: restored}, restoreErr
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
		return referenceShapeWritten{}, fmt.Errorf("write %s %s for the reference shape: %w", input.TypeKey, input.ID, err)
	}
	return referenceShapeWritten{Created: 1, Restored: 0}, nil
}

func referenceShapeProps(values map[string]string) map[string]json.RawMessage {
	props := make(map[string]json.RawMessage, len(values)+1)
	for name, value := range values {
		props[name] = json.RawMessage(strconv.Quote(value))
	}
	return props
}
