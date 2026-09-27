package foundationdb

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// Register verifies one page against the claim and the stored checkpoint and
// records its document ID before the OpenSearch write. The verification
// covers the desired generation, owner, lease, serving index, revision,
// projection version, access-state generation, write versions, and ordinal.
// The issued key includes the projection version. A registration writes a
// new issued key and never replaces an issued ID. When an earlier owner registered the same ordinal
// of this revision under another projection, Register returns
// ErrContentChanged. The worker then schedules a new revision, and cleanup
// retires every issued ID of the older revision.
func (s *SearchWorkStore) Register(ctx context.Context, work searchdomain.Work, page node.ContentPage) (intent searchdomain.WriteIntent, err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.register")(&err)
	if pageErr := searchdomain.ValidatePage(work, page); pageErr != nil {
		return intent, searchStorageError(ctx, "search.work.page_invalid", "validate page intent", work.NodeID, pageErr)
	}
	revision, parseErr := strconv.ParseInt(page.Revision, 10, 64)
	if parseErr != nil {
		return intent, searchStorageError(ctx, "search.work.revision_invalid", "parse page revision", work.NodeID, parseErr)
	}
	documentID := searchdomain.DocumentID(work.OrgID, work.NodeID, page.Revision, page.ProjectionVersion, page.Ordinal)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if err := s.verifyPage(ctx, tr, work, page, revision); err != nil {
			return err
		}
		if err := requireOneProjection(ctx, tr, work, page, revision); err != nil {
			return err
		}
		tr.Set(fdb.Key(searchIssuedKey(work.OrgID, work.NodeID, revision, page.Ordinal, page.ProjectionVersion)), []byte(documentID))
		return nil
	})
	if errors.Is(err, node.ErrContentChanged) {
		return intent, err
	}
	if err != nil {
		return intent, searchStorageError(ctx, "search.work.register_failed", "register page intent", work.NodeID, err)
	}
	return searchdomain.WriteIntent{Work: work, Page: page, DocumentID: documentID}, nil
}

// requireOneProjection returns ErrContentChanged when an issued ID of the
// page's revision and ordinal uses another projection version.
func requireOneProjection(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, page node.ContentPage, revision int64) error {
	keyRange, err := fdb.PrefixRange(searchIssuedOrdinalPrefix(work.OrgID, work.NodeID, revision, page.Ordinal))
	if err != nil {
		return searchReadFailure(ctx, "create issued ordinal range", err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: maxIndexedMatches}).GetSliceWithError()
	if err != nil {
		return searchReadFailure(ctx, "read issued ordinal", err)
	}
	for _, item := range items {
		document, decodeErr := decodeIssuedDocument(ctx, item)
		if decodeErr != nil {
			return decodeErr
		}
		if document.Projection != page.ProjectionVersion {
			return node.ErrContentChanged
		}
	}
	return nil
}

// verifyPage requires page to be the next page of the claimed revision.
func (s *SearchWorkStore) verifyPage(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, page node.ContentPage, revision int64) error {
	record, err := s.verifyClaim(ctx, tr, work)
	if err != nil {
		return err
	}
	current, err := readSearchCounter(ctx, tr, searchRevisionKey(work.OrgID, work.NodeID))
	if err != nil {
		return err
	}
	if record.Revision != revision || current != revision {
		return searchdomain.ErrWorkChanged
	}
	progress, err := s.readProgress(ctx, tr, work.Class, record)
	if err != nil {
		return err
	}
	if progress.Phase != string(searchdomain.PhasePages) || page.Ordinal != progress.Ordinal {
		return searchdomain.ErrWorkChanged
	}
	if progress.Ordinal > 0 && page.ProjectionVersion != progress.Projection {
		return searchdomain.ErrWorkChanged
	}
	var access searchAccessRecord
	found, err := readSearchRecord(ctx, tr, searchAccessKey(work.OrgID, work.NodeID), &access)
	if err != nil {
		return err
	}
	if !found || !slices.Equal(access.Access.Versions, page.Access.Versions) {
		return searchdomain.ErrWorkChanged
	}
	return nil
}
