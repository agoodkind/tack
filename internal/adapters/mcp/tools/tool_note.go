package tools

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

// toolNote stores the outcome, reason, and target that one tool call reports
// for its audit result event. The first reported outcome wins.
type toolNote struct {
	mu      sync.Mutex
	outcome audit.OutcomeDetails
	reason  string
	target  *audit.Target
}

type toolNoteKey struct{}

// idempotentMatchReason is the result reason of a create that matched an
// earlier create with the same idempotency key and wrote nothing.
const idempotentMatchReason = "idempotent_match"

// withToolNote attaches an empty note for one tool call.
func withToolNote(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolNoteKey{}, &toolNote{
		mu: sync.Mutex{}, outcome: audit.NoOutcome(), reason: "", target: nil,
	})
}

// noteToolResult stores outcome, reason, and target in the note on ctx.
// It keeps an outcome that the call already stored and replaces the target
// only when target is not nil.
func noteToolResult(ctx context.Context, outcome audit.OutcomeDetails, reason string, target *audit.Target) {
	note, _ := ctx.Value(toolNoteKey{}).(*toolNote)
	if note == nil {
		return
	}
	note.mu.Lock()
	defer note.mu.Unlock()
	if note.outcome.Outcome == "" {
		note.outcome = outcome
		note.reason = reason
	}
	if target != nil {
		stored := *target
		note.target = &stored
	}
}

// toolNoteResult returns the outcome, reason, and target that the call stored.
func toolNoteResult(ctx context.Context) (audit.OutcomeDetails, string, *audit.Target) {
	note, _ := ctx.Value(toolNoteKey{}).(*toolNote)
	if note == nil {
		return audit.NoOutcome(), "", nil
	}
	note.mu.Lock()
	defer note.mu.Unlock()
	return note.outcome, note.reason, note.target
}

// noteOutcome stores an unsuccessful outcome for the request's read event and
// for the tool call's result event.
func noteOutcome(ctx context.Context, outcome audit.Outcome, code, message string) {
	audit.NoteReadOutcome(ctx, outcome, code, message)
	noteToolResult(ctx, audit.OutcomeDetails{
		Outcome: outcome, FailureCode: code, FailureMessage: message, CommittedSteps: nil,
	}, "", nil)
}

// relationshipTarget returns the audit target of one relationship.
func relationshipTarget(sourceID uuid.UUID, relationType string, targetID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "", URI: "",
		RelationType: relationType, ID: uuid.Nil, SourceID: sourceID, TargetID: targetID,
	}
}

// nodeTarget returns the audit target of one node.
func nodeTarget(nodeType string, nodeID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindNode, NodeType: nodeType, Reference: "", Name: "", URI: "",
		RelationType: "", ID: nodeID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}
