package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// maxSubtreeDeleteJobPage bounds the job records one SubtreeDeletes call reads.
const maxSubtreeDeleteJobPage = 100

// StartSubtreeDelete stores the record of a new subtree delete job with the
// root as the only stack entry, zero deleted and moved nodes, and the root's
// hierarchy parent as the destination of moved children. The step that deletes
// the root writes the job's audit template, so after the record commits the
// start marks the staged event on ctx written. The MCP wrapper then records
// no second event for the delete, even when the request returns before the
// root is gone.
func (s *NodeDeleteStore) StartSubtreeDelete(ctx context.Context, job *node.SubtreeDeleteJob) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.subtree_delete_start")(&err)
	job.Stack = []uuid.UUID{job.RootID}
	job.Deleted, job.Moved = 0, 0
	job.FinishedAt = time.Time{}
	err = runNodeMutation(ctx, s.nodes.db, "start subtree delete of node "+job.RootID.String(), func(tr fdb.Transaction) error {
		parentID, readErr := readRootParent(ctx, tr, job.OrgID, job.RootID)
		if readErr != nil {
			return readErr
		}
		job.RootParentID = parentID
		return writeDeleteJob(ctx, tr, job, s.nodes.clock.Now())
	})
	if err != nil {
		return err
	}
	if len(job.AuditTemplate) > 0 {
		commitStagedIntent(ctx)
	}
	return nil
}

// SubtreeDelete reads one job record. A missing record returns nil.
func (s *NodeDeleteStore) SubtreeDelete(ctx context.Context, jobID uuid.UUID) (job *node.SubtreeDeleteJob, err error) {
	defer telemetry.FDBOp(ctx, "store.node.subtree_delete_read")(&err)
	err = runNodeReadTransaction(ctx, s.nodes.db, "read subtree delete job "+jobID.String(), func(tr fdb.Transaction) error {
		var readErr error
		job, readErr = readDeleteJob(ctx, tr, jobID)
		return readErr
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

// ClearSubtreeDelete removes the record of one finished job. A running job
// keeps its record.
func (s *NodeDeleteStore) ClearSubtreeDelete(ctx context.Context, jobID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.subtree_delete_clear")(&err)
	return runNodeMutation(ctx, s.nodes.db, "clear subtree delete job "+jobID.String(), func(tr fdb.Transaction) error {
		job, readErr := readDeleteJob(ctx, tr, jobID)
		if readErr != nil || job == nil || !job.Finished() {
			return readErr
		}
		tr.Clear(fdb.Key(nodeDeleteJobKey(jobID)))
		return nil
	})
}

// SubtreeDeletes reads at most limit job records after the job ID after, in
// job ID order. uuid.Nil reads from the first job.
func (s *NodeDeleteStore) SubtreeDeletes(ctx context.Context, after uuid.UUID, limit int) (jobs []*node.SubtreeDeleteJob, err error) {
	defer telemetry.FDBOp(ctx, "store.node.subtree_delete_list")(&err)
	if limit < 1 || limit > maxSubtreeDeleteJobPage {
		wrapped := fmt.Errorf("list subtree delete jobs: limit must be between 1 and %d", maxSubtreeDeleteJobPage)
		slog.ErrorContext(ctx, "node.subtree_delete.list_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	keyRange, err := fdb.PrefixRange(nodeDeleteJobPrefix())
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create subtree delete job range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if after != uuid.Nil {
		begin = fdb.FirstGreaterThan(fdb.Key(nodeDeleteJobKey(after)))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	var items []fdb.KeyValue
	err = runNodeReadTransaction(ctx, s.nodes.db, "list subtree delete jobs", func(tr fdb.Transaction) error {
		var readErr error
		items, readErr = tr.GetRange(selection, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read subtree delete jobs", readErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	jobs = make([]*node.SubtreeDeleteJob, 0, len(items))
	for _, item := range items {
		job, decodeErr := decodeDeleteJob(ctx, item.Value)
		if decodeErr != nil {
			return nil, decodeErr
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// readDeleteJob reads one job record inside tr. A missing record returns nil.
func readDeleteJob(ctx context.Context, tr fdb.Transaction, jobID uuid.UUID) (*node.SubtreeDeleteJob, error) {
	encoded, err := tr.Get(fdb.Key(nodeDeleteJobKey(jobID))).Get()
	if err != nil {
		return nil, searchReadFailure(ctx, "read subtree delete job "+jobID.String(), err)
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	return decodeDeleteJob(ctx, encoded)
}

// writeDeleteJob stamps job with now and stores its record inside tr.
func writeDeleteJob(ctx context.Context, tr fdb.Transaction, job *node.SubtreeDeleteJob, now time.Time) error {
	job.UpdatedAt = now.UTC()
	encoded, err := json.Marshal(job)
	if err != nil {
		return nodeOperationFailure(ctx, "encode subtree delete job "+job.ID.String(), err)
	}
	tr.Set(fdb.Key(nodeDeleteJobKey(job.ID)), encoded)
	return nil
}

func decodeDeleteJob(ctx context.Context, encoded []byte) (*node.SubtreeDeleteJob, error) {
	var job node.SubtreeDeleteJob
	if err := json.Unmarshal(encoded, &job); err != nil {
		return nil, nodeOperationFailure(ctx, "decode subtree delete job", err)
	}
	return &job, nil
}
