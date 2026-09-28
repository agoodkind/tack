package foundationdb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// VerifyRebuildDocuments reads one page of at most limit issued documents
// after the verification cursor. The page spans every organization, and the
// limit applies to the whole page. It returns the documents of the current
// revision of each node with the write versions of its authority.
func (s *SearchRebuildStore) VerifyRebuildDocuments(ctx context.Context, _ searchdomain.Work, read searchdomain.Rebuild, limit int) (batch searchdomain.RebuildBatch, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.verify_documents")(&err)
	if limit < 1 || limit > maxSearchDocumentBatch {
		return batch, searchStorageError(ctx, "search.rebuild.limit_invalid", "validate verification batch", uuid.Nil, fmt.Errorf("limit must be between 1 and %d", maxSearchDocumentBatch))
	}
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		items, readErr := readIssuedPage(ctx, tr, read.VerifyCursor, limit)
		if readErr != nil {
			return readErr
		}
		batch = searchdomain.RebuildBatch{Documents: make([]searchdomain.RebuildDocument, 0, limit), NextCursor: "", Done: len(items) <= limit}
		examined := items[:min(len(items), limit)]
		versions := make(map[uuid.UUID][]string)
		for _, item := range examined {
			document, current, decodeErr := currentRebuildDocument(ctx, tr, item, versions)
			if decodeErr != nil {
				return decodeErr
			}
			if current {
				batch.Documents = append(batch.Documents, document)
			}
		}
		if !batch.Done {
			batch.NextCursor = base64.RawURLEncoding.EncodeToString(examined[len(examined)-1].Key)
		}
		return nil
	})
	if err != nil {
		return batch, searchStorageError(ctx, "search.rebuild.verify_read_failed", "read replacement verification documents", uuid.Nil, err)
	}
	return batch, nil
}

func readIssuedPage(ctx context.Context, tr fdb.Transaction, cursor string, limit int) ([]fdb.KeyValue, error) {
	keyRange, err := fdb.PrefixRange(searchIssuedFamilyPrefix())
	if err != nil {
		return nil, searchReadFailure(ctx, "create issued document range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return nil, searchReadFailure(ctx, "decode verification cursor", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	items, err := tr.GetRange(selection, fdb.RangeOptions{Limit: limit + 1}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read issued document page", err)
	}
	return items, nil
}

// currentRebuildDocument decodes one issued key and reports whether its
// revision is the node's current revision. versions caches the write
// versions of each authority within one batch.
func currentRebuildDocument(ctx context.Context, tr fdb.Transaction, item fdb.KeyValue, versions map[uuid.UUID][]string) (searchdomain.RebuildDocument, bool, error) {
	var none searchdomain.RebuildDocument
	document, err := decodeIssuedDocument(ctx, item)
	if err != nil {
		return none, false, err
	}
	values, err := tuple.Unpack(stripPrefix(item.Key))
	if err != nil {
		return none, false, searchReadFailure(ctx, "unpack issued key", err)
	}
	orgText, orgOK := values[1].(string)
	nodeText, nodeOK := values[2].(string)
	orgID, orgErr := uuid.Parse(orgText)
	nodeID, nodeErr := uuid.Parse(nodeText)
	if !orgOK || !nodeOK || orgErr != nil || nodeErr != nil {
		return none, false, searchReadFailure(ctx, "decode issued key", errors.New("issued key identity is invalid"))
	}
	revision, err := readSearchCounter(ctx, tr, searchRevisionKey(orgID, nodeID))
	if err != nil || revision != document.Revision {
		return none, false, err
	}
	required, cached := versions[orgID]
	if !cached {
		rollout, rolloutErr := readRollout(ctx, tr, orgID)
		if rolloutErr != nil {
			return none, false, rolloutErr
		}
		required = slices.Sorted(slices.Values(rollout.WriteVersions))
		versions[orgID] = required
	}
	return searchdomain.RebuildDocument{OrgID: orgID, NodeID: nodeID, Document: document, Versions: required}, true, nil
}

// CompleteRebuildVerify checks one verified batch. A node that already has
// pending work receives no repair. For every other node, a page missing from
// the target schedules a copy of the node, and a page with other write
// versions schedules access work that updates every page of the node. The
// verification then waits at the same cursor. A clean last batch starts the
// switch. It reports whether the replacement waits.
func (s *SearchRebuildStore) CompleteRebuildVerify(ctx context.Context, work searchdomain.Work, read searchdomain.Rebuild, batch searchdomain.RebuildBatch, states map[string]searchdomain.DocumentAccess) (waiting bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rebuild.complete_verify")(&err)
	err = s.rebuildStep(ctx, work, read, func(tr fdb.Transaction, _ searchWorkRecord) error {
		if read.State != searchdomain.RebuildVerifying {
			return searchdomain.ErrWorkChanged
		}
		mismatched := mismatchedRebuildNodes(batch.Documents, states)
		if len(mismatched) > 0 {
			waiting = true
			return scheduleRebuildRepairs(ctx, tr, s.work.clock.Now().UTC(), mismatched)
		}
		next := read
		next.VerifyCursor = batch.NextCursor
		if batch.Done {
			next.State, next.VerifyCursor = searchdomain.RebuildSwitching, ""
		}
		return writeSearchRecord(ctx, tr, searchRebuildKey(), rebuildRecordFor(next))
	})
	if err != nil {
		return false, searchStorageError(ctx, "search.rebuild.verify_failed", "checkpoint replacement verification", uuid.Nil, err)
	}
	return waiting, nil
}

// rebuildRepair is one node the target lacks or stores with other versions.
type rebuildRepair struct {
	orgID   uuid.UUID
	missing bool
}

func mismatchedRebuildNodes(documents []searchdomain.RebuildDocument, states map[string]searchdomain.DocumentAccess) map[uuid.UUID]rebuildRepair {
	mismatched := make(map[uuid.UUID]rebuildRepair)
	for _, document := range documents {
		state := states[document.Document.DocumentID]
		stored := slices.Sorted(slices.Values(state.Versions))
		if state.Found && !state.Retired && slices.Equal(stored, document.Versions) {
			continue
		}
		missing := !state.Found || state.Retired || mismatched[document.NodeID].missing
		mismatched[document.NodeID] = rebuildRepair{orgID: document.OrgID, missing: missing}
	}
	return mismatched
}

func scheduleRebuildRepairs(ctx context.Context, tr fdb.Transaction, now time.Time, mismatched map[uuid.UUID]rebuildRepair) error {
	for nodeID, repair := range mismatched {
		pending := false
		for _, class := range searchNodeClasses {
			_, found, err := readSearchWork(ctx, tr, class, repair.orgID, nodeID)
			if err != nil {
				return err
			}
			pending = pending || found
		}
		if pending {
			continue
		}
		if repair.missing {
			if err := scheduleCopy(ctx, tr, now, repair.orgID, nodeID); err != nil {
				return err
			}
			continue
		}
		if err := scheduleAccessRepair(ctx, tr, now, repair.orgID, nodeID); err != nil {
			return err
		}
	}
	return nil
}
