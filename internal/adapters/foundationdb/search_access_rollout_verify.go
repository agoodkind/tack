package foundationdb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// VerifyDocuments reads at most limit issued documents of the authority
// after the rollout's verification cursor. It reports whether the batch is
// the last one.
func (s *SearchAccessRolloutStore) VerifyDocuments(ctx context.Context, work searchdomain.Work, read searchdomain.AccessRollout, limit int) (documents []searchdomain.RolloutDocument, done bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.verify_documents")(&err)
	if limit < 1 || limit > maxSearchDocumentBatch {
		return nil, false, searchStorageError(ctx, "search.rollout.limit_invalid", "validate verification batch", uuid.Nil, fmt.Errorf("limit must be between 1 and %d", maxSearchDocumentBatch))
	}
	keyRange, err := fdb.PrefixRange(searchIssuedOrgPrefix(work.OrgID))
	if err != nil {
		return nil, false, searchStorageError(ctx, "search.rollout.range_failed", "create verification range", uuid.Nil, err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if read.VerifyCursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(read.VerifyCursor)
		if decodeErr != nil {
			return nil, false, searchStorageError(ctx, "search.rollout.cursor_invalid", "decode verification cursor", uuid.Nil, decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	var items []fdb.KeyValue
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		items, readErr = tr.GetRange(fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}, fdb.RangeOptions{Limit: limit + 1}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read issued documents of authority "+work.OrgID.String(), readErr)
		}
		return nil
	})
	if err != nil {
		return nil, false, searchStorageError(ctx, "search.rollout.documents_failed", "read verification documents", uuid.Nil, err)
	}
	done = len(items) <= limit
	documents = make([]searchdomain.RolloutDocument, 0, min(len(items), limit))
	for _, item := range items[:min(len(items), limit)] {
		document, decodeErr := decodeRolloutDocument(ctx, item)
		if decodeErr != nil {
			return nil, false, searchStorageError(ctx, "search.rollout.document_invalid", "decode verification document", uuid.Nil, decodeErr)
		}
		documents = append(documents, document)
	}
	return documents, done, nil
}

// decodeRolloutDocument decodes one issued key and its document ID. The
// node is the third tuple element: (family, org, node, revision, ordinal,
// projection).
func decodeRolloutDocument(ctx context.Context, item fdb.KeyValue) (searchdomain.RolloutDocument, error) {
	document, err := decodeIssuedDocument(ctx, item)
	if err != nil {
		return searchdomain.RolloutDocument{}, err
	}
	values, err := tuple.Unpack(stripPrefix(item.Key))
	if err != nil {
		return searchdomain.RolloutDocument{}, searchReadFailure(ctx, "unpack issued key", err)
	}
	nodeText, isText := values[2].(string)
	nodeID, parseErr := uuid.Parse(nodeText)
	if !isText || parseErr != nil {
		return searchdomain.RolloutDocument{}, searchReadFailure(ctx, "decode issued node", errors.New("issued key has no node identifier"))
	}
	return searchdomain.RolloutDocument{NodeID: nodeID, Document: document}, nil
}

// CompleteVerify checks one verified batch against the rollout's exact
// write versions. A mismatched page keeps the cursor and schedules access
// or content work for its node unless page work is already pending. The
// last batch compares the permission event boundary, then activates the
// candidate or completes the retirement.
func (s *SearchAccessRolloutStore) CompleteVerify(ctx context.Context, work searchdomain.Work, read searchdomain.AccessRollout, documents []searchdomain.RolloutDocument, states map[string]searchdomain.DocumentAccess, done bool) (step searchdomain.RolloutStep, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.complete_verify")(&err)
	step = searchdomain.RolloutContinue
	err = s.rolloutStep(ctx, work, read, func(tr fdb.Transaction, record *searchRolloutRecord, workRecord searchWorkRecord) error {
		mismatched := mismatchedNodes(documents, states, record.WriteVersions)
		if len(mismatched) > 0 {
			step = searchdomain.RolloutWait
			return scheduleRolloutRepairs(ctx, tr, s.work.clock.Now(), work.OrgID, mismatched)
		}
		if !done {
			last := documents[len(documents)-1]
			key := searchIssuedKey(work.OrgID, last.NodeID, last.Document.Revision, last.Document.Ordinal, last.Document.Projection)
			record.VerifyCursor = base64.RawURLEncoding.EncodeToString(key)
			return nil
		}
		event, eventErr := readPermissionEvent(ctx, tr, work.OrgID)
		if eventErr != nil {
			return eventErr
		}
		if event != record.PermissionEventVersion {
			record.PermissionEventVersion, record.VerifyCursor = event, ""
			return nil
		}
		if record.Phase == string(searchdomain.AccessRetiring) {
			*record = completedRollout(*record)
			clearSearchWork(tr, work, workRecord)
			step = searchdomain.RolloutComplete
			return nil
		}
		record.PreviousVersion, record.ActiveVersion = record.ActiveVersion, record.CandidateVersion
		return nil
	})
	if err != nil {
		return step, searchStorageError(ctx, "search.rollout.verify_failed", "checkpoint access verification of authority "+work.OrgID.String(), uuid.Nil, err)
	}
	telemetry.L(ctx).DebugContext(ctx, "search.rollout.verified", slog.String("authority_id", work.OrgID.String()),
		slog.Int("documents", len(documents)), slog.Int("step", int(step)))
	return step, nil
}

// mismatchedNodes returns each node with a page that lacks the exact write
// versions. The value reports whether OpenSearch has no active document.
func mismatchedNodes(documents []searchdomain.RolloutDocument, states map[string]searchdomain.DocumentAccess, required []string) map[uuid.UUID]bool {
	mismatched := make(map[uuid.UUID]bool)
	for _, document := range documents {
		state := states[document.Document.DocumentID]
		versions := slices.Clone(state.Versions)
		slices.Sort(versions)
		if state.Found && !state.Retired && slices.Equal(versions, required) {
			continue
		}
		mismatched[document.NodeID] = mismatched[document.NodeID] || !state.Found || state.Retired
	}
	return mismatched
}

// scheduleRolloutRepairs schedules content work for a node without an active
// page document and access work for any other mismatched node. A node with
// pending live, access, or cleanup work waits for that work instead.
func scheduleRolloutRepairs(ctx context.Context, tr fdb.Transaction, now time.Time, orgID uuid.UUID, mismatched map[uuid.UUID]bool) error {
	for nodeID, missing := range mismatched {
		pending := false
		for _, class := range searchNodeClasses {
			_, found, err := readSearchWork(ctx, tr, class, orgID, nodeID)
			if err != nil {
				return err
			}
			pending = pending || found
		}
		if pending {
			continue
		}
		change := searchChangeAccess
		if missing {
			change = searchChangeContent
		}
		if err := scheduleExistingSearchChange(ctx, tr, now, orgID, nodeID, change); err != nil {
			return err
		}
	}
	return nil
}

// completedRollout returns the stable state after a finished retirement.
func completedRollout(record searchRolloutRecord) searchRolloutRecord {
	record.CandidateVersion, record.PreviousVersion = "", ""
	record.WriteVersions = []string{record.ActiveVersion}
	record.Phase = string(searchdomain.AccessStable)
	record.ScanCursor, record.VerifyCursor, record.RetireCursor, record.ScanComplete = "", "", "", false
	return record
}
