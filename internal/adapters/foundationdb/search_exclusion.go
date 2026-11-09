package foundationdb

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// searchFailureLimit is the number of counted failures at or above which ops
// search verify lists a work item as stuck. Release logs the item once when
// its count equals the limit. The item stays pending and keeps retrying in
// every class. The worker counts a failed FoundationDB read, access
// compilation, or page encoding. The worker never counts a failed OpenSearch
// operation. Work stays pending throughout an engine outage, an HTTP 429
// from the ML Commons memory circuit breaker, or a model redeploy that
// causes it to fail.
const searchFailureLimit = 5

// failureOutcome reports the result of recording one failure. LimitCrossed
// reports whether the counted failure set the work item's attempt count to
// searchFailureLimit. Attempts is the count after the failure. ItemID
// identifies the item.
type failureOutcome struct {
	LimitCrossed bool
	Attempts     int64
	ItemID       string
}

// searchExclusionRecord is the exclusion of one node. Class and Index are
// the work class and physical index of the failure that excluded the node.
type searchExclusionRecord struct {
	Class      string    `json:"class"`
	Index      string    `json:"index"`
	Reason     string    `json:"reason"`
	ExcludedAt time.Time `json:"excluded_at"`
}

// Exclude excludes the node of the claimed work from search and records
// reason in its exclusion.
//
// The transaction verifies the claim before scheduling exclusion and saving
// its record. Only live, access, and copy work for an existing node can
// exclude that node.
//
//   - The exclusion record stores the class, physical index, reason, and
//     exclusion time.
func (s *SearchWorkStore) Exclude(ctx context.Context, work searchdomain.Work, reason string) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.exclude")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, err := s.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		return s.excludeNode(ctx, tr, work, record, reason)
	})
	if err != nil {
		return searchStorageError(ctx, "search.work.exclude_failed", "exclude node from search", work.NodeID, err)
	}
	logSearchExclusion(ctx, work, reason)
	return nil
}

// recordFailure increments attempts for a counted failure, stores the
// message, and keeps work pending. The next claim waits for the longer of
// searchRetryDelay and failure.RetryAfter.
//
// A nonnil failure.Pending preserves its document and index record under the
// work class and node. The outcome reports whether the failure sets the
// count to the limit.
func (s *SearchWorkStore) recordFailure(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, failure searchdomain.Failure) (failureOutcome, error) {
	outcome := failureOutcome{LimitCrossed: false, Attempts: 0, ItemID: ""}
	if failure.Counted {
		var err error
		outcome, err = countFailure(ctx, tr, work)
		if err != nil {
			return outcome, err
		}
	}
	if failure.Pending != nil {
		if err := recordPendingWrite(ctx, tr, work, *failure.Pending); err != nil {
			return outcome, err
		}
	}
	tr.Set(fdb.Key(searchErrorKey(string(work.Class), work.OrgID, work.NodeID)), []byte(failure.Message))
	return outcome, writeSearchRecord(ctx, tr, searchClaimKey(string(work.Class), searchBucket(work.OrgID, work.NodeID), work.OrgID, work.NodeID), searchClaimRecord{
		Owner: "", Generation: work.Generation, LeaseUntil: s.clock.Now().Add(max(searchRetryDelay, failure.RetryAfter)), Target: work.Target, Mirror: work.Mirror,
	})
}

// countFailure increments the work item's attempt count. It marks the
// failure that sets the count to searchFailureLimit and identifies the item.
func countFailure(ctx context.Context, tr fdb.Transaction, work searchdomain.Work) (failureOutcome, error) {
	outcome := failureOutcome{LimitCrossed: false, Attempts: 0, ItemID: ""}
	attempts, err := incrementSearchCounter(ctx, tr, searchAttemptKey(string(work.Class), work.OrgID, work.NodeID))
	if err != nil {
		return outcome, err
	}
	outcome.Attempts = attempts
	if attempts != searchFailureLimit {
		return outcome, nil
	}
	outcome.LimitCrossed = true
	outcome.ItemID, err = searchItemID(ctx, tr, work.Class, work.OrgID, work.NodeID)
	return outcome, err
}

// excludeNode clears the claimed work and schedules an empty content
// revision. It removes live, access, and copy work. Cleanup retires every
// node page in the serving index and in a replacement that copies, verifies,
// or switches.
//
// excludeNode clears recorded access keys and pending flags before saving
// the exclusion. A later compilation that produces different versions or
// keys schedules the node's dependents through access work.
func (s *SearchWorkStore) excludeNode(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, record searchWorkRecord, reason string) error {
	if !work.Excludable() {
		return fmt.Errorf("%s work of node %s cannot exclude the node", work.Class, work.NodeID)
	}
	clearSearchWork(tr, work, record)
	for _, class := range searchNodeClasses {
		tr.Clear(fdb.Key(searchErrorKey(string(class), work.OrgID, work.NodeID)))
		tr.Clear(fdb.Key(searchAttemptKey(string(class), work.OrgID, work.NodeID)))
	}
	now := s.clock.Now().UTC()
	if _, err := scheduleSearchChange(ctx, tr, now, work.OrgID, work.NodeID, searchChangeExclusion); err != nil {
		return err
	}
	var state searchAccessRecord
	if _, err := readSearchRecord(ctx, tr, searchAccessKey(work.OrgID, work.NodeID), &state); err != nil {
		return err
	}
	state.Access.Keys = []string{}
	state.PagesPending, state.DependentsPending = false, false
	if err := writeSearchRecord(ctx, tr, searchAccessKey(work.OrgID, work.NodeID), state); err != nil {
		return err
	}
	return writeSearchRecord(ctx, tr, searchExclusionKey(work.OrgID, work.NodeID), searchExclusionRecord{
		Class: string(work.Class), Index: work.Target, Reason: reason, ExcludedAt: now,
	})
}

// exclusionAdjustedChange selects the change to schedule for one node. An
// access change for an excluded node requests repair because access work
// cannot update that node's absent pages. A deletion clears the exclusion.
func exclusionAdjustedChange(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID, change searchChange) (searchChange, error) {
	if change == searchChangeDeletion {
		tr.Clear(fdb.Key(searchExclusionKey(orgID, nodeID)))
		return change, nil
	}
	if change != searchChangeAccess {
		return change, nil
	}
	encoded, err := tr.Get(fdb.Key(searchExclusionKey(orgID, nodeID))).Get()
	if err != nil {
		return change, searchReadFailure(ctx, "read search exclusion of node "+nodeID.String(), err)
	}
	if len(encoded) > 0 {
		return searchChangeRepair, nil
	}
	return change, nil
}

// logFailureOutcome logs the work item once when its attempt count equals
// the limit. The log includes the item's generation, index, and failure
// message.
func logFailureOutcome(ctx context.Context, work searchdomain.Work, outcome failureOutcome, message string) {
	if !outcome.LimitCrossed {
		return
	}
	telemetry.L(ctx).ErrorContext(ctx, "search.work.attempt_limit_crossed", slog.String("err", message),
		slog.String("class", string(work.Class)), slog.String("item_id", outcome.ItemID), slog.Int64("attempts", outcome.Attempts),
		slog.Int64("generation", work.Generation), slog.String("index", work.Target))
}

func logSearchExclusion(ctx context.Context, work searchdomain.Work, reason string) {
	telemetry.L(ctx).ErrorContext(ctx, "search.work.node_excluded", slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.String("index", work.Target), slog.String("reason", reason))
}
