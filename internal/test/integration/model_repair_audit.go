package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	appruntime "goodkind.io/tack/internal/runtime"
)

// modelRepairOutboxPage is the number of outbox entries that one read returns.
const modelRepairOutboxPage = 500

// modelRepairStep is the step evidence that a repair deploy records.
type modelRepairStep struct {
	ModelID string `json:"model_id"`
	TaskID  string `json:"task_id"`
}

// ModelRepairEvents returns the position after the last entry of the
// FoundationDB operator outbox and the events after mark that the model
// repair service recorded. A nil mark reads from the first entry.
func ModelRepairEvents(t *testing.T, stores *fdbadapter.Stores, mark []byte) ([]byte, []audit.Event) {
	t.Helper()
	repairID := cli.ServiceActorID(appruntime.SearchModelRepairService)
	var events []audit.Event
	for {
		entries, err := stores.OpsOutbox.ReadOutboxFrom(WithTestOperation(t, t.Context()), mark, modelRepairOutboxPage)
		if err != nil {
			t.Fatalf("reading the FoundationDB operator outbox failed: %v", err)
		}
		if len(entries) == 0 {
			return mark, events
		}
		for _, entry := range entries {
			var event audit.Event
			if json.Unmarshal(entry.Event, &event) == nil && event.Actor.ID == repairID {
				events = append(events, event)
			}
		}
		mark = entries[len(entries)-1].Mark
	}
}

// CompletedModelRepair returns nil when events contain a completed repair
// operation on modelID with a completed step that records a deploy task.
func CompletedModelRepair(events []audit.Event, modelID string) error {
	reference := "opensearch/model/" + modelID
	steps := map[uuid.UUID]bool{}
	for _, event := range events {
		eventContext := event.Context
		if event.Verb != string(audit.VerbSearchModelRepair) || eventContext.Phase != audit.PhaseResult ||
			event.Outcome != audit.OutcomeCompleted || eventContext.ParentOperationID == uuid.Nil ||
			eventContext.Target == nil || eventContext.Target.Reference != reference {
			continue
		}
		var step modelRepairStep
		if json.Unmarshal(event.Extra, &step) == nil && step.ModelID == modelID && step.TaskID != "" {
			steps[eventContext.ParentOperationID] = true
		}
	}
	for _, event := range events {
		eventContext := event.Context
		if eventContext.Phase == audit.PhaseResult && eventContext.ParentOperationID == uuid.Nil &&
			event.Outcome == audit.OutcomeCompleted && steps[eventContext.OperationID] {
			return nil
		}
	}
	if len(steps) == 0 {
		return errors.New("the outbox does not contain a completed repair step that records a deploy task")
	}
	return errors.New("the outbox does not contain a completed result for a repair operation with a recorded deploy step")
}
