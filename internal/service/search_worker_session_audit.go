package service

import (
	"context"
	"encoding/json"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// retirementEvidence identifies the index replacement that retires sessions
// and the operator who began it.
type retirementEvidence struct {
	RebuildID   string `json:"rebuild_id"`
	SourceIndex string `json:"source_index"`
	initiationEvidence
}

// retirementContext attaches a new worker operation to ctx for the session
// changes of one retirement slice. The operation uses the worker actor, and
// its parent is the operation that began the replacement.
func (w *SearchWorker) retirementContext(ctx context.Context, rebuild searchdomain.Rebuild) (context.Context, error) {
	operation, err := audit.NewMutationOperation(w.ports.Audit.Actor, audit.SourceSystem, "")
	if err != nil {
		return ctx, structureFailure(ctx, "create the session retirement operation", err)
	}
	extra, err := json.Marshal(retirementEvidence{
		RebuildID: rebuild.ID.String(), SourceIndex: rebuild.SourceIndex, initiationEvidence: initiatedBy(rebuild.Initiation),
	})
	if err != nil {
		return ctx, structureFailure(ctx, "encode the session retirement evidence", err)
	}
	operation.Context.ParentOperationID = rebuild.Initiation.OperationID
	operation.Extra = extra
	attached, err := audit.WithMutationContext(ctx, operation)
	if err != nil {
		return ctx, structureFailure(ctx, "attach the session retirement operation", err)
	}
	return attached, nil
}
