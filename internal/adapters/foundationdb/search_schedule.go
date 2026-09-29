package foundationdb

import (
	"context"
	"crypto/sha256"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// searchWorkBuckets is the number of independent claim buckets. The first
// SHA-256 byte of the organization and node identity selects the bucket.
const searchWorkBuckets = 256

// searchChange classifies one source mutation for search scheduling.
type searchChange uint8

// searchChangeExclusion starts an empty content revision for a node that
// search cannot index. searchChangeRepair replaces an access change of an
// excluded node and schedules content and access work for it.
const (
	searchChangeContent searchChange = iota + 1
	searchChangeAccess
	searchChangeDeletion
	searchChangeExclusion
	searchChangeRepair
)

// searchRecordPlan is the effect of one change on one pending work class.
type searchRecordPlan uint8

const (
	// searchRecordRewrite sets the generation of an existing record to the
	// new generation. It preserves the record's revision and deletion flag
	// and leaves its checkpoint unchanged.
	searchRecordRewrite searchRecordPlan = iota + 1
	// searchRecordRestart sets the generation and revision of an existing
	// record to the new values and clears its checkpoint.
	searchRecordRestart
	// searchRecordPut writes the record and clears its checkpoint.
	searchRecordPut
	// searchRecordRemove clears the record, its claim, and its checkpoint.
	searchRecordRemove
)

var searchNodeClasses = []searchdomain.WorkClass{
	searchdomain.WorkClassLive, searchdomain.WorkClassAccess, searchdomain.WorkClassCleanup, searchdomain.WorkClassCopy,
}

// scheduleSearchChange records desired search work for one node inside the
// caller's source transaction. It increments the node generation once and
// rewrites every pending record of the node to that generation. An access
// change preserves the revision of pending content and cleanup work and
// leaves the content checkpoint unchanged. A content, exclusion, or repair
// change starts a new revision. An access change of an excluded node
// becomes a repair, and a deletion clears the node's exclusion.
// now is the enqueue time read from the caller's injected clock. It returns
// the record of the new generation.
func scheduleSearchChange(ctx context.Context, tr fdb.Transaction, now time.Time, orgID, nodeID uuid.UUID, change searchChange) (searchWorkRecord, error) {
	change, err := exclusionAdjustedChange(ctx, tr, orgID, nodeID, change)
	if err != nil {
		return searchWorkRecord{}, err
	}
	generation, err := incrementSearchCounter(ctx, tr, searchGenerationKey(orgID, nodeID))
	if err != nil {
		return searchWorkRecord{}, err
	}
	revision, err := readSearchCounter(ctx, tr, searchRevisionKey(orgID, nodeID))
	if err != nil {
		return searchWorkRecord{}, err
	}
	if change == searchChangeContent || change == searchChangeExclusion || change == searchChangeRepair {
		revision = generation
		writeSearchCounter(tr, searchRevisionKey(orgID, nodeID), revision)
	}
	current := searchWorkRecord{
		OrgID: orgID, NodeID: nodeID, Generation: generation, Revision: revision,
		Deleted: change == searchChangeDeletion, EnqueuedAt: now.UTC(),
	}
	for _, class := range searchNodeClasses {
		if err := applySearchRecordPlan(ctx, tr, class, searchRecordPlanFor(change, class), current); err != nil {
			return searchWorkRecord{}, err
		}
	}
	if change == searchChangeDeletion {
		return current, nil
	}
	return current, initializeSearchAccess(ctx, tr, orgID, nodeID)
}

// searchRecordPlans maps each change and node work class to its effect. A
// content change removes a pending replacement copy. The worker that claims
// the new live work also writes its pages to the replacement index. An access
// change rewrites the copy at the new generation. An exclusion removes every
// other work item, and its cleanup retires every page of the node. A repair
// writes the node's pages and recompiles its access.
var searchRecordPlans = map[searchChange]map[searchdomain.WorkClass]searchRecordPlan{
	searchChangeContent: {
		searchdomain.WorkClassLive:    searchRecordPut,
		searchdomain.WorkClassAccess:  searchRecordRestart,
		searchdomain.WorkClassCleanup: searchRecordRewrite,
		searchdomain.WorkClassCopy:    searchRecordRemove,
	},
	searchChangeAccess: {
		searchdomain.WorkClassLive:    searchRecordRewrite,
		searchdomain.WorkClassAccess:  searchRecordPut,
		searchdomain.WorkClassCleanup: searchRecordRewrite,
		searchdomain.WorkClassCopy:    searchRecordRewrite,
	},
	searchChangeDeletion: {
		searchdomain.WorkClassLive:    searchRecordRemove,
		searchdomain.WorkClassAccess:  searchRecordRemove,
		searchdomain.WorkClassCleanup: searchRecordPut,
		searchdomain.WorkClassCopy:    searchRecordRemove,
	},
	searchChangeExclusion: {
		searchdomain.WorkClassLive:    searchRecordRemove,
		searchdomain.WorkClassAccess:  searchRecordRemove,
		searchdomain.WorkClassCleanup: searchRecordPut,
		searchdomain.WorkClassCopy:    searchRecordRemove,
	},
	searchChangeRepair: {
		searchdomain.WorkClassLive:    searchRecordPut,
		searchdomain.WorkClassAccess:  searchRecordPut,
		searchdomain.WorkClassCleanup: searchRecordRewrite,
		searchdomain.WorkClassCopy:    searchRecordRemove,
	},
}

func searchRecordPlanFor(change searchChange, class searchdomain.WorkClass) searchRecordPlan {
	return searchRecordPlans[change][class]
}

func applySearchRecordPlan(
	ctx context.Context,
	tr fdb.Transaction,
	class searchdomain.WorkClass,
	plan searchRecordPlan,
	current searchWorkRecord,
) error {
	bucket := searchBucket(current.OrgID, current.NodeID)
	cursorKey := fdb.Key(searchCursorKey(string(class), current.OrgID, current.NodeID))
	existing, found, err := readSearchWork(ctx, tr, class, current.OrgID, current.NodeID)
	if err != nil {
		return err
	}
	if plan == searchRecordRemove {
		if found {
			removeSearchWork(tr, class, existing)
		}
		tr.Clear(fdb.Key(searchClaimKey(string(class), bucket, current.OrgID, current.NodeID)))
		tr.Clear(cursorKey)
		return nil
	}
	if !found && plan != searchRecordPut {
		return nil
	}
	next := current
	var previous *searchWorkRecord
	if found {
		previous = &existing
	}
	if plan == searchRecordRewrite {
		next.Revision = existing.Revision
		next.Deleted = existing.Deleted
	} else {
		tr.Clear(cursorKey)
	}
	return writeSearchWork(ctx, tr, class, next, previous)
}

func earliestSearchTime(first, second time.Time) time.Time {
	if first.IsZero() || second.Before(first) {
		return second
	}
	return first
}

// searchBucket returns the first SHA-256 byte of the organization and node
// identity. Each organization and node pair has one bucket. Different nodes
// of one organization can have different buckets.
func searchBucket(orgID, nodeID uuid.UUID) int {
	var identity [32]byte
	copy(identity[:16], orgID[:])
	copy(identity[16:], nodeID[:])
	digest := sha256.Sum256(identity[:])
	return int(digest[0])
}
