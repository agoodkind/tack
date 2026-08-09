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

func seedReferenceCounters(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	execute bool,
) (int, error) {
	counters, err := enumerateReferenceCounters(ctx, env, orgID, nil)
	if err != nil {
		return 0, err
	}
	if !execute {
		return len(counters), nil
	}
	for _, counter := range counters {
		if err := env.Stores.Nodes.SeedSequenceByKey(ctx, orgID, counter.Key, counter.Value); err != nil {
			wrapped := fmt.Errorf("seed counter %q in org %s: %w", counter.Key, orgID, err)
			env.Log.WarnContext(
				ctx, "repair.reference_uniqueness.counter_seed_failed",
				slog.String("org_id", orgID.String()),
				slog.String("counter_key", counter.Key),
				slog.String("err", wrapped.Error()),
			)
			return 0, wrapped
		}
	}
	return len(counters), nil
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
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.types_failed",
			slog.String("org_id", orgID.String()), slog.String("err", wrapped.Error()))
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
		views, listErr := env.Stores.Views.List(ctx, node.NodeListQuery{
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
		})
		if listErr != nil {
			wrapped := fmt.Errorf("list %s nodes in org %s: %w", nodeType.TypeKey, orgID, listErr)
			env.Log.WarnContext(
				ctx, "repair.reference_uniqueness.views_failed",
				slog.String("org_id", orgID.String()),
				slog.String("node_type", nodeType.TypeKey),
				slog.String("err", wrapped.Error()),
			)
			return nil, wrapped
		}
		for _, view := range views {
			if recordErr := recordHighestGenerated(
				ctx, env, orgID, typeIndex, nodeType, *template, view, highest, scopeCache,
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
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.counter_key_failed",
			slog.String("node_id", view.ID.String()), slog.String("err", wrapped.Error()))
		return wrapped
	}
	value, err := numberPropValue(view.Props, template.Generated)
	if err != nil {
		wrapped := fmt.Errorf("read generated value for node %s: %w", view.ID, err)
		env.Log.WarnContext(ctx, "repair.reference_uniqueness.generated_value_unreadable",
			slog.String("node_id", view.ID.String()), slog.String("err", wrapped.Error()))
		return wrapped
	}
	if value > highest[counterKey] {
		highest[counterKey] = value
	}
	return nil
}

// numberPropValue reads the generated property as the number the node holds.
// A missing property yields zero: the node carries no number to protect. A
// present value that cannot be read is an error, because counting it as zero
// would leave the counter below a number already in use and the next
// allocation would hand out that number again.
func numberPropValue(props map[string]json.RawMessage, name string) (int64, error) {
	raw := props[name]
	if len(raw) == 0 {
		return 0, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	// Rendering accepts a numeric string, so seeding reads one too; the two
	// must agree on what counts as the node's number.
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		parsed, parseErr := strconv.ParseInt(text, 10, 64)
		if parseErr == nil {
			return parsed, nil
		}
	}
	return 0, fmt.Errorf("property %q holds %s, which is not a whole number", name, raw)
}
