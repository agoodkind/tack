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

// searchFailureLimit is the number of counted failures after which Release
// excludes the node of live, access, or copy work. Every other work item
// keeps retrying, and ops search verify reports it once its count equals the
// limit. The worker counts a failed FoundationDB read, access compilation,
// or page encoding. Each of those repeats on every retry until the node, its
// hierarchy, or its metadata changes. The worker never counts a failed
// OpenSearch operation. Work that an engine outage, an HTTP 429 from the ML
// Commons memory circuit breaker, or a model redeploy fails stays pending for
// the whole failure.
const searchFailureLimit = 5

// failureOutcome is the result of one recorded failure. Excluded reports an
// excluded node. LimitCrossed reports the failure that set the attempt count
// of a work item that stays pending to searchFailureLimit. ItemID identifies
// that item.
type failureOutcome struct {
	Excluded     bool
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

// recordFailure increments the attempt count of a counted failure. When the
// count of excludable work equals searchFailureLimit, it excludes the node.
// Otherwise it stores the failure message and delays the next claim by
// searchRetryDelay. The outcome reports the failure that sets the count of
// other work to the limit.
func (s *SearchWorkStore) recordFailure(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, record searchWorkRecord, failure searchdomain.Failure) (failureOutcome, error) {
	outcome := failureOutcome{Excluded: false, LimitCrossed: false, Attempts: 0, ItemID: ""}
	if failure.Counted {
		var err error
		outcome, err = countFailure(ctx, tr, work)
		if err != nil {
			return outcome, err
		}
		if outcome.Excluded {
			return outcome, s.excludeNode(ctx, tr, work, record, failure.Message)
		}
	}
	tr.Set(fdb.Key(searchErrorKey(string(work.Class), work.OrgID, work.NodeID)), []byte(failure.Message))
	return outcome, writeSearchRecord(ctx, tr, searchClaimKey(string(work.Class), searchBucket(work.OrgID, work.NodeID), work.OrgID, work.NodeID), searchClaimRecord{
		Owner: "", Generation: work.Generation, LeaseUntil: s.clock.Now().Add(searchRetryDelay), Target: work.Target, Mirror: work.Mirror,
	})
}

// countFailure increments the attempt count of work. The outcome excludes
// excludable work at searchFailureLimit. For other work, it marks the
// failure that sets the count to the limit and identifies the item.
func countFailure(ctx context.Context, tr fdb.Transaction, work searchdomain.Work) (failureOutcome, error) {
	outcome := failureOutcome{Excluded: false, LimitCrossed: false, Attempts: 0, ItemID: ""}
	attempts, err := incrementSearchCounter(ctx, tr, searchAttemptKey(string(work.Class), work.OrgID, work.NodeID))
	if err != nil {
		return outcome, err
	}
	outcome.Attempts = attempts
	if work.Excludable() {
		outcome.Excluded = attempts >= searchFailureLimit
		return outcome, nil
	}
	if attempts != searchFailureLimit {
		return outcome, nil
	}
	outcome.LimitCrossed = true
	outcome.ItemID, err = searchItemID(ctx, tr, work.Class, work.OrgID, work.NodeID)
	return outcome, err
}

// excludeNode clears the claimed work and schedules an exclusion change. The
// change starts an empty content revision and removes the node's live,
// access, and copy work. Its cleanup work retires every page of the node in
// the serving index, and in the replacement index while a replacement
// copies, verifies, or switches. excludeNode clears the recorded access keys.
// The next successful compilation then differs from the recorded access, and
// the access work of that compilation schedules the node's dependents.
// excludeNode records the exclusion last.
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

// exclusionAdjustedChange returns the change to schedule for one node. An
// excluded node has no page for access work to update, and an access change
// of an excluded node becomes a repair. A deletion clears the node's
// exclusion.
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

// logFailureOutcome logs an exclusion, or the work item with an attempt
// count that crossed the limit, with the failure message.
func logFailureOutcome(ctx context.Context, work searchdomain.Work, outcome failureOutcome, message string) {
	if outcome.Excluded {
		logSearchExclusion(ctx, work, message)
	}
	if outcome.LimitCrossed {
		telemetry.L(ctx).ErrorContext(ctx, "search.work.attempt_limit_crossed", slog.String("err", message),
			slog.String("class", string(work.Class)), slog.String("item_id", outcome.ItemID), slog.Int64("attempts", outcome.Attempts))
	}
}

func logSearchExclusion(ctx context.Context, work searchdomain.Work, reason string) {
	telemetry.L(ctx).ErrorContext(ctx, "search.work.node_excluded", slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.String("index", work.Target), slog.String("reason", reason))
}
