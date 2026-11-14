package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

type referenceCounter struct {
	Key   string
	Value int64
}

func enumerateReferenceCounters(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	createdBefore *time.Time,
) ([]referenceCounter, error) {
	nodeTypes, err := env.Stores.NodeTypes.List(ctx, orgID)
	if err != nil {
		wrapped := fmt.Errorf("list node types for org %s: %w", orgID, err)
		env.Log.WarnContext(
			ctx,
			"repair.reference_uniqueness.types_failed",
			slog.String("org_id", orgID.String()),
			slog.String("err", wrapped.Error()),
		)

		return nil, wrapped
	}

	typeIndex := node.BuildTypeIndex(nodeTypes)
	highest := make(map[string]int64)
	scopeCache := make(map[uuid.UUID]map[string]string)

	for _, nodeType := range nodeTypes {
		template := nodeType.PrimaryReferenceTemplate()
		if template == nil || template.Generated == "" {
			continue
		}

		views, listErr := env.Stores.Views.List(
			ctx,
			node.NodeListQuery{
				OrgID:            orgID,
				NodeType:         nodeType.TypeKey,
				ByProperty:       nil,
				BySourceRelation: nil,
				ByTargetRelation: nil,
				CreatedAfter:     nil,
				CreatedBefore:    createdBefore,
				PropFilters:      nil,
				Limit:            0,
				Cursor:           "",
			},
		)
		if listErr != nil {
			wrapped := fmt.Errorf("list %s nodes in org %s: %w", nodeType.TypeKey, orgID, listErr)
			env.Log.WarnContext(
				ctx,
				"repair.reference_uniqueness.views_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_type", nodeType.TypeKey),
				slog.String("err", wrapped.Error()),
			)

			return nil, wrapped
		}

		for _, view := range views {
			if recordErr := recordHighestGenerated(
				ctx,
				env,
				orgID,
				typeIndex,
				nodeType,
				*template,
				view,
				highest,
				scopeCache,
			); recordErr != nil {
				return nil, recordErr
			}
		}
	}

	counters := make([]referenceCounter, 0, len(highest))
	for counterKey, value := range highest {
		counters = append(counters, referenceCounter{Key: counterKey, Value: value})
	}

	sort.Slice(counters, func(i, j int) bool { return counters[i].Key < counters[j].Key })

	return counters, nil
}

func recordHighestGenerated(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	typeIndex map[string]*node.NodeType,
	nodeType *node.NodeType,
	template node.ReferenceTemplate,
	view *node.NodeView,
	highest map[string]int64,
	scopeCache map[uuid.UUID]map[string]string,
) error {
	scopeRefs, err := scopeReferencesForView(ctx, env, orgID, typeIndex, view, scopeCache)
	if err != nil {
		return err
	}

	counterKey, err := template.CounterKey(node.ReferenceRenderInput{
		NodeTypeKey: nodeType.TypeKey,
		Props:       view.Props,
		ScopeRefs:   scopeRefs,
	})
	if err != nil {
		wrapped := fmt.Errorf("derive counter key for node %s: %w", view.ID, err)
		env.Log.WarnContext(
			ctx,
			"repair.reference_uniqueness.counter_key_failed",
			slog.String("node_id", view.ID.String()),
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	value, err := numberPropValue(view.Props, template.Generated)
	if err != nil {
		wrapped := fmt.Errorf("read generated value for node %s: %w", view.ID, err)
		env.Log.WarnContext(
			ctx,
			"repair.reference_uniqueness.generated_value_unreadable",
			slog.String("node_id", view.ID.String()),
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	if value > highest[counterKey] {
		highest[counterKey] = value
	}

	return nil
}

// numberPropValue reads the generated property as an integer. A missing
// property yields zero. An unreadable value returns an error to prevent
// resetting the counter below a number already in use and allocating it again.
func numberPropValue(props map[string]json.RawMessage, name string) (int64, error) {
	raw := props[name]
	if len(raw) == 0 {
		return 0, nil
	}

	var value int64
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}

	// Seeding and rendering both accept numeric strings as node numbers.
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, parseErr := strconv.ParseInt(text, 10, 64)
		if parseErr == nil {
			return parsed, nil
		}
	}

	return 0, fmt.Errorf("property %q contains %s, which is not a whole number", name, raw)
}
