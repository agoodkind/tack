package integration_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// fixedDefinitionsCommandResult is the part of the ops backfill
// once-fixed-definitions result that the tests read.
type fixedDefinitionsCommandResult struct {
	DryRun    bool                        `json:"dry_run"`
	NodeTypes service.FixedNodeTypeResult `json:"node_types"`
}

// TestFixedDefinitionsCorrectsEarlierEpicCanLiveUnder stores the epic node
// type of the test organization with the earlier seeded CanLiveUnder list
// [project]. A second organization stores the customized list
// [project cycle]. Through ops backfill once-fixed-definitions, the dry run
// must report the exact before and after lists for both types and write
// neither. The execute run must store the seeded list for the first
// organization, change only can_live_under in the stored record, and stage
// one node_type.update event with that delta. The second organization must
// stay unchanged and appear as conflicted in every run. A second execute run
// must leave the corrected epic type unchanged.
func TestFixedDefinitionsCorrectsEarlierEpicCanLiveUnder(t *testing.T) {
	env := integration.SetupTestEnv(t)
	epicType := storedNodeType(t, env, "epic")
	seeded := slices.Clone(epicType.CanLiveUnder)
	if !slices.Contains(seeded, "epic") {
		t.Fatalf("seeded epic CanLiveUnder = %v, want a list with epic", seeded)
	}
	earlier, custom := []string{"project"}, []string{"project", "cycle"}
	epicType.CanLiveUnder = earlier
	if err := env.Stores.NodeTypes.Set(env.Ctx, epicType); err != nil {
		t.Fatalf("store the earlier epic node type: %v", err)
	}
	customOrgID := uuid.Must(uuid.NewV7())
	if err := env.Seeder.SeedOrg(env.Ctx, customOrgID); err != nil {
		t.Fatalf("seed organization %s: %v", customOrgID, err)
	}
	customType := storedOrgNodeType(t, env, customOrgID, "epic")
	customType.CanLiveUnder = custom
	if err := env.Stores.NodeTypes.Set(env.Ctx, customType); err != nil {
		t.Fatalf("store the customized epic node type: %v", err)
	}
	corrected := service.FixedNodeTypeChange{
		OrgID: env.OrgID, NodeTypeID: epicType.ID, TypeKey: "epic", Outcome: service.FixedOutcomeCorrected,
		CanLiveUnder: service.FixedListChange{Before: earlier, After: seeded}, Reason: "",
	}

	preview := runFixedDefinitions(t)
	requireNodeTypeChange(t, "dry run", preview.NodeTypes.Changes, corrected)
	requireCustomEpicConflict(t, "dry run", preview.NodeTypes, customType.ID, custom, seeded)
	if got := storedNodeType(t, env, "epic").CanLiveUnder; !preview.DryRun || !slices.Equal(got, earlier) {
		t.Fatalf("dry run = %t, epic CanLiveUnder after it = %v, want a dry run that keeps %v", preview.DryRun, got, earlier)
	}

	before, mark := storedKeyValues(t), outboxTail(t, env)
	applied := runFixedDefinitions(t, "--execute")
	requireNodeTypeChange(t, "execute run", applied.NodeTypes.Changes, corrected)
	requireCustomEpicConflict(t, "execute run", applied.NodeTypes, customType.ID, custom, seeded)
	if got := storedNodeType(t, env, "epic").CanLiveUnder; !slices.Equal(got, seeded) {
		t.Fatalf("epic CanLiveUnder after the execute run = %v, want %v", got, seeded)
	}
	if got := storedOrgNodeType(t, env, customOrgID, "epic").CanLiveUnder; !slices.Equal(got, custom) {
		t.Fatalf("customized epic CanLiveUnder after the execute run = %v, want %v", got, custom)
	}
	events := outboxEventsAfter(t, env, mark)
	if updates := changeEventsFor(events, audit.VerbNodeTypeUpdate, customType.ID); len(updates) != 0 {
		t.Fatalf("customized epic type change events = %+v, want none", updates)
	}
	requireCanLiveUnderDelta(t, events, before, storedKeyValues(t), epicType.ID, earlier, seeded)

	repeated := runFixedDefinitions(t, "--execute")
	if slices.Contains(repeated.NodeTypes.CorrectedIDs, epicType.ID) || slices.Contains(repeated.NodeTypes.ConflictedIDs, epicType.ID) {
		t.Fatalf("second execute run = %+v, want epic type %s unchanged", repeated.NodeTypes, epicType.ID)
	}
	requireCustomEpicConflict(t, "second execute run", repeated.NodeTypes, customType.ID, custom, seeded)
}

func runFixedDefinitions(t *testing.T, args ...string) fixedDefinitionsCommandResult {
	t.Helper()
	var result fixedDefinitionsCommandResult
	if err := runBackfillCommand(t, "once-fixed-definitions", &result, args...); err != nil {
		t.Fatalf("ops backfill once-fixed-definitions %v: %v", args, err)
	}
	return result
}

// requireCustomEpicConflict requires result to report node type typeID as
// conflicted with the stored and seeded lists and a reason that names the
// earlier seeded value.
func requireCustomEpicConflict(t *testing.T, run string, result service.FixedNodeTypeResult, typeID uuid.UUID, stored, seeded []string) {
	t.Helper()
	if !slices.Contains(result.ConflictedIDs, typeID) || slices.Contains(result.CorrectedIDs, typeID) {
		t.Fatalf("%s = %+v, want node type %s conflicted and not corrected", run, result, typeID)
	}
	for _, change := range result.Changes {
		if change.NodeTypeID != typeID {
			continue
		}
		if change.Outcome != service.FixedOutcomeConflicted || !slices.Equal(change.CanLiveUnder.Before, stored) ||
			!slices.Equal(change.CanLiveUnder.After, seeded) || !strings.Contains(change.Reason, "earlier seeded value") {
			t.Fatalf("%s change for node type %s = %+v, want a conflict from %v to %v with a reason", run, typeID, change, stored, seeded)
		}
		return
	}
	t.Fatalf("%s changes %+v do not include node type %s", run, result.Changes, typeID)
}

// requireCanLiveUnderDelta requires events to hold exactly one
// node_type.update event for typeID. Its delta must change only
// can_live_under from earlier to seeded and copy the bytes of every other
// field. The stored record must change from the delta's Before bytes to its
// After bytes.
func requireCanLiveUnderDelta(t *testing.T, events []audit.Event, before, after map[string]string, typeID uuid.UUID, earlier, seeded []string) {
	t.Helper()
	updates := changeEventsFor(events, audit.VerbNodeTypeUpdate, typeID)
	if len(updates) != 1 || updates[0].Delta == nil {
		t.Fatalf("node_type.update events for %s = %+v, want one with a delta", typeID, updates)
	}
	delta := updates[0].Delta
	if !slices.Equal(delta.Changed, []string{"/can_live_under"}) {
		t.Fatalf("node_type.update changed = %v, want only /can_live_under", delta.Changed)
	}
	beforeFields, afterFields := decodeTypeFields(t, delta.Before), decodeTypeFields(t, delta.After)
	if !slices.Equal(decodeTypeList(t, beforeFields), earlier) || !slices.Equal(decodeTypeList(t, afterFields), seeded) {
		t.Fatalf("node_type.update can_live_under %s to %s, want %v to %v", beforeFields["can_live_under"], afterFields["can_live_under"], earlier, seeded)
	}
	delete(beforeFields, "can_live_under")
	delete(afterFields, "can_live_under")
	if !maps.EqualFunc(beforeFields, afterFields, func(left, right json.RawMessage) bool { return string(left) == string(right) }) {
		t.Fatalf("node_type.update changed fields other than can_live_under: %s to %s", delta.Before, delta.After)
	}
	for key, value := range after {
		if value == string(delta.After) && before[key] == string(delta.Before) {
			return
		}
	}
	t.Fatalf("no stored record changed from the delta's Before %s to its After %s", delta.Before, delta.After)
}

func decodeTypeFields(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode node type record %s: %v", raw, err)
	}
	return fields
}

func decodeTypeList(t *testing.T, fields map[string]json.RawMessage) []string {
	t.Helper()
	var list []string
	if err := json.Unmarshal(fields["can_live_under"], &list); err != nil {
		t.Fatalf("decode can_live_under %s: %v", fields["can_live_under"], err)
	}
	return list
}

// storedOrgNodeType returns the stored node type for typeKey in orgID.
func storedOrgNodeType(t *testing.T, env *integration.TestEnv, orgID uuid.UUID, typeKey string) *node.NodeType {
	t.Helper()
	types, err := env.Stores.NodeTypes.List(env.Ctx, orgID)
	if err != nil {
		t.Fatalf("list node types of %s: %v", orgID, err)
	}
	for _, nodeType := range types {
		if nodeType.TypeKey == typeKey {
			return nodeType
		}
	}
	t.Fatalf("organization %s has no node type %s", orgID, typeKey)
	return nil
}
