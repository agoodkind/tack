package foundationdb

import (
	"bytes"
	"context"
	"encoding/base64"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// affectedDocuments reads at most limit issued documents of the first node
// in the affected set after the verification cursor.
func (s *SearchAccessRolloutStore) affectedDocuments(ctx context.Context, work searchdomain.Work, read searchdomain.AccessRollout, limit int) ([]searchdomain.RolloutDocument, bool, error) {
	var items []fdb.KeyValue
	err := transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		nodeID, present, err := firstRolloutAffected(ctx, tr, work.OrgID, read.Generation)
		if err != nil || !present {
			items = nil
			return err
		}
		keyRange, begin, err := affectedPageRange(ctx, work.OrgID, nodeID, read.VerifyCursor)
		if err != nil {
			return err
		}
		items, err = tr.GetRange(fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}, fdb.RangeOptions{Limit: limit + 1}).GetSliceWithError()
		if err != nil {
			return searchReadFailure(ctx, "read issued documents of affected node "+nodeID.String(), err)
		}
		return nil
	})
	if err != nil {
		return nil, false, searchStorageError(ctx, "search.rollout.affected_documents_failed", "read affected node documents", uuid.Nil, err)
	}
	documents := make([]searchdomain.RolloutDocument, 0, min(len(items), limit))
	for _, item := range items[:min(len(items), limit)] {
		document, decodeErr := decodeRolloutDocument(ctx, item)
		if decodeErr != nil {
			return nil, false, searchStorageError(ctx, "search.rollout.document_invalid", "decode affected node document", uuid.Nil, decodeErr)
		}
		documents = append(documents, document)
	}
	return documents, len(items) <= limit, nil
}

// affectedPageRange returns one node's issued page range and the selector
// after the cursor. A cursor for another node selects the first page.
func affectedPageRange(ctx context.Context, orgID, nodeID uuid.UUID, cursor string) (fdb.KeyRange, fdb.KeySelector, error) {
	prefix := searchIssuedPrefix(orgID, nodeID)
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return keyRange, fdb.FirstGreaterOrEqual(nil), searchReadFailure(ctx, "create affected node page range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor == "" {
		return keyRange, begin, nil
	}
	lastKey, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return keyRange, begin, searchReadFailure(ctx, "decode affected node cursor", err)
	}
	if bytes.HasPrefix(lastKey, prefix) {
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	return keyRange, begin, nil
}

// completeAffected runs one affected pass step. It restarts the authority
// scan when the set exceeds the bound, activates the candidate when the set
// is empty, and otherwise verifies the first node. It returns the set size
// when it restarts the scan.
func completeAffected(
	ctx context.Context,
	tr fdb.Transaction,
	work searchdomain.Work,
	record *searchRolloutRecord,
	documents []searchdomain.RolloutDocument,
	states map[string]searchdomain.DocumentAccess,
	now time.Time,
) (searchdomain.RolloutStep, int64, error) {
	size, err := readRolloutAffectedCount(ctx, tr, work.OrgID, record.Generation)
	if err != nil {
		return searchdomain.RolloutContinue, 0, err
	}
	if size > searchRolloutAffectedLimit {
		return searchdomain.RolloutContinue, size, restartRolloutScan(ctx, tr, record)
	}
	nodeID, present, err := firstRolloutAffected(ctx, tr, work.OrgID, record.Generation)
	if err != nil {
		return searchdomain.RolloutContinue, 0, err
	}
	if !present {
		record.PreviousVersion, record.ActiveVersion = record.ActiveVersion, record.CandidateVersion
		record.AffectedPass, record.VerifyCursor = false, ""
		return searchdomain.RolloutContinue, 0, clearRolloutAffected(ctx, tr, work.OrgID, record.Generation)
	}
	step, err := verifyAffectedNode(ctx, tr, work, record, nodeID, documents, states, now)
	return step, 0, err
}

// verifyAffectedNode verifies one batch of pages of the first affected node
// and removes the node after its last page passes. It also removes a node
// that no longer resolves.
func verifyAffectedNode(
	ctx context.Context,
	tr fdb.Transaction,
	work searchdomain.Work,
	record *searchRolloutRecord,
	nodeID uuid.UUID,
	documents []searchdomain.RolloutDocument,
	states map[string]searchdomain.DocumentAccess,
	now time.Time,
) (searchdomain.RolloutStep, error) {
	for _, document := range documents {
		if document.NodeID != nodeID {
			record.VerifyCursor = ""
			return searchdomain.RolloutContinue, nil
		}
	}
	resolved, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return searchdomain.RolloutContinue, searchReadFailure(ctx, "read node resolution "+nodeID.String()+" for an access rollout", err)
	}
	if len(resolved) == 0 {
		removeRolloutAffected(tr, work.OrgID, record.Generation, nodeID)
		record.VerifyCursor = ""
		return searchdomain.RolloutContinue, nil
	}
	nodeState, err := readRolloutNodeState(ctx, tr, work.OrgID, nodeID)
	if err != nil {
		return searchdomain.RolloutContinue, err
	}
	stale, err := staleRolloutNodes(ctx, tr, work.OrgID, documents, states, record.WriteVersions)
	if err != nil {
		return searchdomain.RolloutContinue, err
	}
	if nodeState.pending {
		stale[nodeID] = rolloutRepairWait
	}
	if len(stale) > 0 {
		return searchdomain.RolloutWait, scheduleRolloutRepairs(ctx, tr, now, work.OrgID, stale)
	}
	cursor := record.VerifyCursor
	if len(documents) > 0 {
		last := documents[len(documents)-1]
		cursor = base64.RawURLEncoding.EncodeToString(searchIssuedKey(work.OrgID, nodeID, last.Document.Revision, last.Document.Ordinal, last.Document.Projection))
	}
	keyRange, begin, err := affectedPageRange(ctx, work.OrgID, nodeID, cursor)
	if err != nil {
		return searchdomain.RolloutContinue, err
	}
	more, err := tr.GetRange(fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
	if err != nil {
		return searchdomain.RolloutContinue, searchReadFailure(ctx, "read issued documents of affected node "+nodeID.String(), err)
	}
	if len(more) > 0 {
		record.VerifyCursor = cursor
		if len(documents) == 0 {
			record.VerifyCursor = ""
		}
		return searchdomain.RolloutContinue, nil
	}
	removeRolloutAffected(tr, work.OrgID, record.Generation, nodeID)
	record.VerifyCursor = ""
	return searchdomain.RolloutContinue, nil
}

// restartRolloutScan clears the affected set and its counter, returns the
// rollout to backfill with a cleared scan cursor, and records the current
// permission event boundary.
func restartRolloutScan(ctx context.Context, tr fdb.Transaction, record *searchRolloutRecord) error {
	if err := clearRolloutAffected(ctx, tr, record.AuthorityID, record.Generation); err != nil {
		return err
	}
	event, err := readPermissionEvent(ctx, tr, record.AuthorityID)
	if err != nil {
		return err
	}
	record.Phase, record.ScanCursor, record.VerifyCursor = string(searchdomain.AccessBackfill), "", ""
	record.AffectedPass, record.PermissionEventVersion = false, event
	return nil
}
