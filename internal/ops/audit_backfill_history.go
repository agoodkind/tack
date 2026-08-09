package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// seedNodeTypeWorkspace matches service.nodeTypeWorkspace.
const seedNodeTypeWorkspace = "workspace"

func deriveReferenceRepairHistory(
	ctx context.Context,
	env *Env,
	principal audit.OperatorPrincipal,
	occurredAt time.Time,
) (auditBackfillPlan, error) {
	renameClass, orgID, err := deriveReferenceRenameClass(ctx, env, principal, occurredAt)
	if err != nil {
		return auditBackfillPlan{}, err
	}
	if err := verifyProductionSeedIdentity(ctx, env, orgID); err != nil {
		return auditBackfillPlan{}, err
	}
	counterClass, err := deriveCounterSeedClass(ctx, env, principal, orgID, occurredAt, referenceRepairStart)
	if err != nil {
		return auditBackfillPlan{}, err
	}
	keyClasses, err := deriveReferenceKeyClasses(ctx, env, principal, orgID, occurredAt, referenceRepairStart)
	if err != nil {
		return auditBackfillPlan{}, err
	}
	seedClasses, err := deriveSeedClasses(ctx, principal, orgID, occurredAt)
	if err != nil {
		return auditBackfillPlan{}, err
	}
	classes := []auditBackfillClass{renameClass, counterClass}
	classes = append(classes, keyClasses...)
	classes = append(classes, seedClasses...)
	return auditBackfillPlan{Classes: classes}, nil
}

func deriveReferenceRenameClass(
	ctx context.Context,
	env *Env,
	principal audit.OperatorPrincipal,
	occurredAt time.Time,
) (auditBackfillClass, uuid.UUID, error) {
	renames, err := loadReferenceRenameEvidence(ctx)
	if err != nil {
		return auditBackfillClass{}, uuid.Nil, err
	}
	events := make([]audit.Event, 0, len(renames))
	orgID := uuid.Nil
	for _, rename := range renames {
		event, eventErr := referenceRenameEvent(ctx, env.Stores.Views, principal, rename, occurredAt)
		if eventErr != nil {
			return auditBackfillClass{}, uuid.Nil, eventErr
		}
		if orgID != uuid.Nil && orgID != event.Context.OrgID {
			err := fmt.Errorf("reference rename evidence spans orgs %s and %s", orgID, event.Context.OrgID)
			telemetry.L(ctx).Error("audit.reference_repair_backfill.org_mismatch", slog.String("err", err.Error()))
			return auditBackfillClass{}, uuid.Nil, err
		}
		orgID = event.Context.OrgID
		events = append(events, event)
	}
	// Recorded is the count from the rollout record, not the length of the
	// evidence file. Comparing the file against itself would pass even if the
	// file were truncated, which is the one way this class can silently
	// reconstruct less history than the repair performed.
	return auditBackfillClass{Count: auditBackfillCount{
		Class: "reference renames", Derived: len(events), Recorded: recordedReferenceRenames,
	}, Events: events}, orgID, nil
}

func verifyProductionSeedIdentity(ctx context.Context, env *Env, orgID uuid.UUID) error {
	org, err := env.Stores.Views.Get(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("get production org %s: %w", orgID, err)
		telemetry.L(ctx).Error("audit.reference_repair_backfill.org_get_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	if org == nil || stringProp(org.Props, "slug") != productionSeedOrgSlug {
		err := fmt.Errorf("production seed org %q is not present", productionSeedOrgSlug)
		telemetry.L(ctx).Error("audit.reference_repair_backfill.org_invalid", slog.String("err", err.Error()))
		return err
	}
	workspaces, err := env.Stores.Views.List(ctx, node.NodeListQuery{
		OrgID: orgID, NodeType: seedNodeTypeWorkspace, ByProperty: nil, BySourceRelation: nil,
		ByTargetRelation: nil, CreatedAfter: nil, CreatedBefore: nil, PropFilters: nil, Limit: 0, Cursor: "",
	})
	if err != nil {
		wrapped := fmt.Errorf("list production workspaces: %w", err)
		telemetry.L(ctx).Error("audit.reference_repair_backfill.workspace_list_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	for _, workspace := range workspaces {
		if stringProp(workspace.Props, "slug") == productionSeedWorkspaceSlug {
			return nil
		}
	}
	err = fmt.Errorf("production seed workspace %q is not present", productionSeedWorkspaceSlug)
	telemetry.L(ctx).Error("audit.reference_repair_backfill.workspace_invalid", slog.String("err", err.Error()))
	return err
}

func sortedReferenceKeys(keys []repairedReferenceKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].View.ID != keys[j].View.ID {
			return keys[i].View.ID.String() < keys[j].View.ID.String()
		}
		if keys[i].Key.TemplateName != keys[j].Key.TemplateName {
			return keys[i].Key.TemplateName < keys[j].Key.TemplateName
		}
		return keys[i].Key.Encoded < keys[j].Key.Encoded
	})
}
