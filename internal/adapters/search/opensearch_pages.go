package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// Put writes one complete page at its FoundationDB generation. It validates
// page_text, then reads the stored page in each index. It returns
// ErrObsoleteWrite for a retired page or a stored generation above the write
// generation. It skips pages at the write generation and otherwise indexes
// with if_seq_no and if_primary_term from the read. When intent.Pending is
// non-nil, Put returns ErrPageWritePending for an absent page in
// intent.Pending.Index without sending a write request to that index.
// Put writes the mirror after the target accepts the page and returns the
// total encoded request length in bytes. Each failed index request returns
// a PageWriteError that identifies its physical index.
func (a *Adapter) Put(ctx context.Context, intent searchdomain.WriteIntent) (int, error) {
	if intent.Work.Target == "" {
		return 0, pageWriteFailure(ctx, "search.page.target_missing", intent.Work, errors.New("page write requires a serving index"))
	}
	if err := searchdomain.ValidatePageText(intent.Page.Text); err != nil {
		return 0, pageWriteFailure(ctx, "search.page.text_invalid", intent.Work, err)
	}
	written, err := a.putPage(ctx, intent)
	if err != nil || intent.Work.Mirror == "" {
		return written, err
	}
	mirrored := intent
	mirrored.Work = mirrorWork(intent.Work)
	extra, err := a.putPage(ctx, mirrored)
	return written + extra, err
}

func (a *Adapter) putPage(ctx context.Context, intent searchdomain.WriteIntent) (int, error) {
	held := intent.Pending != nil && intent.Pending.Index == intent.Work.Target
	page := guardedPage{
		documentID: intent.DocumentID, generation: intent.Work.Generation, holdCreate: held,
		encode: func(precondition pagePrecondition) ([]byte, error) {
			return encodePageDocument(ctx, intent, precondition)
		},
	}
	_, written, err := a.writeGuarded(ctx, intent.Work.Target, []guardedPage{page}, staleRefused)
	if err != nil {
		failure := pageWriteFailure(ctx, "search.page.write_failed", intent.Work, err)
		return written, &searchdomain.PageWriteError{Index: intent.Work.Target, Err: failure}
	}
	return written, nil
}

// UpdateAccess changes only search_generation and access on the current
// pages of the target and the mirror index. It sends no page_text and needs
// no deployed model. It returns the length of the prefix both accepted.
func (a *Adapter) UpdateAccess(ctx context.Context, intent searchdomain.AccessIntent) (int, error) {
	accepted, err := a.updateAccessIn(ctx, intent.Work, intent)
	if err != nil || intent.Work.Mirror == "" {
		return accepted, err
	}
	mirrored, err := a.updateAccessIn(ctx, mirrorWork(intent.Work), intent)
	return min(accepted, mirrored), err
}

func (a *Adapter) updateAccessIn(ctx context.Context, work searchdomain.Work, intent searchdomain.AccessIntent) (int, error) {
	operations := make([]bulkOperation, 0, len(intent.Documents))
	for _, document := range intent.Documents {
		encoded, err := encodeAccessUpdate(ctx, work, document.DocumentID, intent.Access)
		if err != nil {
			return 0, pageWriteFailure(ctx, "search.access.encode_failed", work, err)
		}
		operations = append(operations, bulkOperation{documentID: document.DocumentID, encoded: encoded})
	}
	accepted, err := a.submitBulk(ctx, work.Target, operations, bulkAccess)
	if err != nil {
		return accepted, pageWriteFailure(ctx, "search.access.write_failed", work, err)
	}
	return accepted, nil
}

// Retire replaces each obsolete page with a text-free retired record at the
// work generation in the target and the mirror index. A page stored at a
// higher generation stays unchanged and counts as accepted. It returns the
// length of the prefix both accepted.
func (a *Adapter) Retire(ctx context.Context, intent searchdomain.RetirementIntent) (int, error) {
	accepted, err := a.retireIn(ctx, intent.Work, intent.Documents)
	if err != nil || intent.Work.Mirror == "" {
		return accepted, err
	}
	mirrored, err := a.retireIn(ctx, mirrorWork(intent.Work), intent.Documents)
	return min(accepted, mirrored), err
}

func (a *Adapter) retireIn(ctx context.Context, work searchdomain.Work, documents []searchdomain.IssuedDocument) (int, error) {
	pages := make([]guardedPage, 0, len(documents))
	for _, document := range documents {
		pages = append(pages, guardedPage{
			documentID: document.DocumentID, generation: work.Generation, holdCreate: false,
			encode: func(precondition pagePrecondition) ([]byte, error) {
				return encodeRetirement(ctx, work, document, precondition)
			},
		})
	}
	accepted, _, err := a.writeGuarded(ctx, work.Target, pages, staleSuperseded)
	if err != nil {
		return accepted, pageWriteFailure(ctx, "search.retirement.write_failed", work, err)
	}
	return accepted, nil
}

// mirrorWork returns a copy of work with Target set to the mirror index and
// Mirror cleared.
func mirrorWork(work searchdomain.Work) searchdomain.Work {
	work.Target, work.Mirror = work.Mirror, ""
	return work
}

// Refresh makes every completed write in index searchable.
func (a *Adapter) Refresh(ctx context.Context, index string) error {
	response, err := a.api.Indices.Refresh(ctx, &opensearchapi.IndicesRefreshReq{Indices: []string{index}})
	if err != nil {
		wrapped := fmt.Errorf("refresh OpenSearch index %s: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.index.refresh_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	if response.Inspect().Response != nil && response.Inspect().Response.IsError() {
		wrapped := fmt.Errorf("refresh OpenSearch index %s: %s", index, response.Inspect().Response.String())
		telemetry.L(ctx).ErrorContext(ctx, "search.index.refresh_rejected", slog.String("err", wrapped.Error()), slog.String("index", index))
		return wrapped
	}
	return nil
}

func pageWriteFailure(ctx context.Context, event string, work searchdomain.Work, err error) error {
	wrapped := fmt.Errorf("write search pages for node %s at generation %d to %s: %w", work.NodeID, work.Generation, work.Target, err)
	if errors.Is(err, searchdomain.ErrObsoleteWrite) || errors.Is(err, searchdomain.ErrPageWritePending) {
		return wrapped
	}
	telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", wrapped.Error()), slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.Int64("generation", work.Generation), slog.String("index", work.Target), bulkAttr(err))
	return wrapped
}
