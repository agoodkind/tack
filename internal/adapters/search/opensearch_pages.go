package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// Put writes one complete page at its FoundationDB generation with
// version_type external_gte. It validates page_text before any engine
// request and returns the encoded request length in bytes. When the work
// item has a mirror index, Put writes the same page to the mirror index
// after the target accepts it.
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
	encoded, err := encodePageDocument(ctx, intent)
	if err != nil {
		return 0, pageWriteFailure(ctx, "search.page.encode_failed", intent.Work, err)
	}
	operations := []bulkOperation{{documentID: intent.DocumentID, encoded: encoded}}
	if _, err := a.submitBulk(ctx, intent.Work.Target, operations, bulkContent); err != nil {
		return len(encoded), pageWriteFailure(ctx, "search.page.write_failed", intent.Work, err)
	}
	return len(encoded), nil
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
// work generation in the target and the mirror index. It returns the length
// of the prefix both accepted.
func (a *Adapter) Retire(ctx context.Context, intent searchdomain.RetirementIntent) (int, error) {
	accepted, err := a.retireIn(ctx, intent.Work, intent.Documents)
	if err != nil || intent.Work.Mirror == "" {
		return accepted, err
	}
	mirrored, err := a.retireIn(ctx, mirrorWork(intent.Work), intent.Documents)
	return min(accepted, mirrored), err
}

func (a *Adapter) retireIn(ctx context.Context, work searchdomain.Work, documents []searchdomain.IssuedDocument) (int, error) {
	operations := make([]bulkOperation, 0, len(documents))
	for _, document := range documents {
		encoded, err := encodeRetirement(ctx, work, document)
		if err != nil {
			return 0, pageWriteFailure(ctx, "search.retirement.encode_failed", work, err)
		}
		operations = append(operations, bulkOperation{documentID: document.DocumentID, encoded: encoded})
	}
	accepted, err := a.submitBulk(ctx, work.Target, operations, bulkRetirement)
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

// Refresh makes the completed pages of one node searchable in index.
func (a *Adapter) Refresh(ctx context.Context, index string) error {
	response, err := a.api.Indices.Refresh(ctx, &opensearchapi.IndicesRefreshReq{Index: []string{index}})
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
	if errors.Is(err, searchdomain.ErrObsoleteWrite) {
		return wrapped
	}
	telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", wrapped.Error()), slog.String("node_id", work.NodeID.String()),
		slog.String("class", string(work.Class)), slog.Int64("generation", work.Generation), slog.String("index", work.Target))
	return wrapped
}
