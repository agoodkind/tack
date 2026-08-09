package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

type seedPropertyDefs struct {
	inner    node.PropertyDefRepository
	recorder audit.Recorder
}

// Set records the intended change before making it, then records the result.
//
// Recording after the write would leave a change in the database with no
// ledger row whenever the record failed, which is the exact gap this work
// exists to close. Recording first costs a pending row when the write then
// fails, and a pending row that never resolves is a visible loose end rather
// than an invisible one.
func (r seedPropertyDefs) Set(ctx context.Context, propertyDef *node.PropertyDef) error {
	existing, err := r.inner.Get(ctx, propertyDef.OrgID, propertyDef.ID)
	if err != nil {
		return seedRepositoryError(ctx, "get property definition", err)
	}
	verb := audit.VerbPropertyDefCreate
	if existing != nil {
		verb = audit.VerbPropertyDefUpdate
	}
	if err := recordSeedPropertyDefIntent(ctx, r.recorder, verb, propertyDef); err != nil {
		return err
	}
	if err := r.inner.Set(ctx, propertyDef); err != nil {
		return seedRepositoryError(ctx, "set property definition", err)
	}
	return recordSeedPropertyDef(ctx, r.recorder, verb, propertyDef)
}

func (r seedPropertyDefs) Get(ctx context.Context, orgID, propertyDefID uuid.UUID) (*node.PropertyDef, error) {
	propertyDef, err := r.inner.Get(ctx, orgID, propertyDefID)
	if err != nil {
		return nil, seedRepositoryError(ctx, "get property definition", err)
	}
	return propertyDef, nil
}

func (r seedPropertyDefs) List(ctx context.Context, orgID uuid.UUID) ([]*node.PropertyDef, error) {
	propertyDefs, err := r.inner.List(ctx, orgID)
	if err != nil {
		return nil, seedRepositoryError(ctx, "list property definitions", err)
	}
	return propertyDefs, nil
}

func (r seedPropertyDefs) Delete(ctx context.Context, orgID, propertyDefID uuid.UUID) error {
	if err := r.inner.Delete(ctx, orgID, propertyDefID); err != nil {
		return seedRepositoryError(ctx, "delete property definition", err)
	}
	return nil
}

type seedNodeTypes struct {
	inner    node.TypeRepository
	recorder audit.Recorder
}

// Set records the intended change before making it, then records the result,
// for the same reason seedPropertyDefs.Set does.
func (r seedNodeTypes) Set(ctx context.Context, nodeType *node.NodeType) error {
	existing, err := r.inner.Get(ctx, nodeType.OrgID, nodeType.ID)
	if err != nil {
		return seedRepositoryError(ctx, "get node type", err)
	}
	verb := audit.VerbNodeTypeCreate
	if existing != nil {
		verb = audit.VerbNodeTypeUpdate
	}
	if err := recordSeedNodeTypeIntent(ctx, r.recorder, verb, nodeType); err != nil {
		return err
	}
	if err := r.inner.Set(ctx, nodeType); err != nil {
		return seedRepositoryError(ctx, "set node type", err)
	}
	return recordSeedNodeType(ctx, r.recorder, verb, nodeType)
}

func (r seedNodeTypes) Get(ctx context.Context, orgID, typeID uuid.UUID) (*node.NodeType, error) {
	nodeType, err := r.inner.Get(ctx, orgID, typeID)
	if err != nil {
		return nil, seedRepositoryError(ctx, "get node type", err)
	}
	return nodeType, nil
}

func (r seedNodeTypes) List(ctx context.Context, orgID uuid.UUID) ([]*node.NodeType, error) {
	nodeTypes, err := r.inner.List(ctx, orgID)
	if err != nil {
		return nil, seedRepositoryError(ctx, "list node types", err)
	}
	return nodeTypes, nil
}

func (r seedNodeTypes) Delete(ctx context.Context, orgID, typeID uuid.UUID) error {
	if err := r.inner.Delete(ctx, orgID, typeID); err != nil {
		return seedRepositoryError(ctx, "delete node type", err)
	}
	return nil
}

func seedRepositoryError(ctx context.Context, action string, err error) error {
	slog.ErrorContext(ctx, "seed.repository_failed", slog.String("action", action), slog.String("err", err.Error()))
	return fmt.Errorf("seed %s: %w", action, err)
}
