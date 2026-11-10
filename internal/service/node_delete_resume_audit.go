package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

// subtreeDeleteTargetPrefix starts the audit target reference of one subtree
// delete job.
const subtreeDeleteTargetPrefix = "foundationdb/subtree-delete/"

// JobAudit configures the audit recording of a recurring service job. Outbox
// stores the events, and Actor identifies the service that runs the job.
type JobAudit struct {
	Outbox audit.OutboxWriter
	Actor  audit.Actor
}

// subtreeDeleteEvidence is the job state that a resume run records before
// and after it finishes the job.
type subtreeDeleteEvidence struct {
	JobID   string `json:"job_id"`
	RootID  string `json:"root_id"`
	OrgID   string `json:"org_id"`
	State   string `json:"state"`
	Deleted int    `json:"deleted"`
	Moved   int    `json:"moved"`
}

// recordResume runs one resume of job as a service step with recording. The
// step records the stored job state before the resume and the job state that
// the resume returns.
func (s *NodeService) recordResume(ctx context.Context, recording JobAudit, job *node.SubtreeDeleteJob) (DeleteResult, error) {
	planned, err := encodeSubtreeDeleteEvidence(ctx, subtreeDeleteEvidence{
		JobID: job.ID.String(), RootID: job.RootID.String(), OrgID: job.OrgID.String(),
		State: string(job.State()), Deleted: job.Deleted, Moved: job.Moved,
	})
	if err != nil {
		return DeleteResult{}, err
	}
	target := audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Reference: subtreeDeleteTargetPrefix + job.ID.String(), Name: "",
		URI: "", RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	var result DeleteResult
	err = audit.RunServiceStep(ctx, recording.Outbox, recording.Actor, audit.VerbNodeSubtreeDeleteResume, target, planned,
		func(ctx context.Context) error {
			var runErr error
			result, runErr = s.runSubtreeDelete(ctx, job.ID, 0)
			return runErr
		},
		func(ctx context.Context) (json.RawMessage, error) {
			return encodeSubtreeDeleteEvidence(ctx, subtreeDeleteEvidence{
				JobID: job.ID.String(), RootID: job.RootID.String(), OrgID: job.OrgID.String(),
				State: string(result.State), Deleted: result.Deleted, Moved: result.Moved,
			})
		})
	if err != nil {
		wrapped := fmt.Errorf("resume delete job %s: %w", job.ID, err)
		slog.ErrorContext(ctx, "node.subtree_delete.resume_failed", slog.String("err", wrapped.Error()), slog.String("job_id", job.ID.String()))
		return result, wrapped
	}
	return result, nil
}

func encodeSubtreeDeleteEvidence(ctx context.Context, evidence subtreeDeleteEvidence) (json.RawMessage, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		slog.ErrorContext(ctx, "node.subtree_delete.evidence_encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode subtree delete evidence of job %s: %w", evidence.JobID, err)
	}
	return encoded, nil
}
