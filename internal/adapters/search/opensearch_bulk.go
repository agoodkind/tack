package search

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// maxBulkDocuments is the document count one bulk request stays below.
	maxBulkDocuments = 500
	// maxBulkBytes is the NDJSON byte count one bulk request stays below.
	maxBulkBytes = 5 << 20
)

// bulkItemPolicy decides which item results count as accepted.
type bulkItemPolicy uint8

const (
	// bulkGuarded accepts only successful writes. A 409 means another write
	// changed the page after the read that set the request precondition.
	bulkGuarded bulkItemPolicy = iota + 1
	// bulkAccess also accepts a missing document. The later content write of
	// that document compiles current access. An item error with
	// staleGenerationMarker is obsolete; a 409 after retry_on_conflict is a
	// rejection.
	bulkAccess
)

// bulkOperation is one NDJSON-encoded action and document.
type bulkOperation struct {
	documentID string
	encoded    []byte
}

// submitBulk sends operations with the typed bulk API in requests below 500
// documents and 5 MiB. One operation of 5 MiB or more forms a request by
// itself. It stops at the first rejected item and returns the length of the
// accepted contiguous prefix.
func (a *Adapter) submitBulk(ctx context.Context, index string, operations []bulkOperation, policy bulkItemPolicy) (int, error) {
	accepted := 0
	for start := 0; start < len(operations); {
		end, body := nextBulkRequest(operations, start)
		count, err := a.sendBulk(ctx, index, body, operations[start:end], policy)
		accepted += count
		if err != nil {
			return accepted, err
		}
		start = end
	}
	return accepted, nil
}

// nextBulkRequest concatenates encoded operations from start. It stops
// before the request would contain 500 documents or 5 MiB. One operation of
// 5 MiB or more forms a request by itself.
func nextBulkRequest(operations []bulkOperation, start int) (int, []byte) {
	var body bytes.Buffer
	end := start
	for end < len(operations) && end-start < maxBulkDocuments-1 {
		if end > start && body.Len()+len(operations[end].encoded) >= maxBulkBytes {
			break
		}
		body.Write(operations[end].encoded)
		end++
	}
	return end, body.Bytes()
}

// sendBulk sends one bulk request and measures its round trip.
// After policy accepts every item, sendBulk logs search.bulk.completed
// at Debug. On failure, it returns a bulkRequestError with the measurement.
func (a *Adapter) sendBulk(ctx context.Context, index string, body []byte, operations []bulkOperation, policy bulkItemPolicy) (int, error) {
	started := clock.Now()
	response, err := a.api.Bulk(ctx, opensearchapi.BulkReq{Index: index, Body: bytes.NewReader(body)})
	measured := bulkMeasurement{duration: clock.Since(started), pages: len(operations), bytes: len(body)}
	if err != nil && (!opensearchapi.IsPartialFailure(err) || response == nil) {
		wrapped := fmt.Errorf("send OpenSearch bulk request to %s: %w", index, err)
		telemetry.L(ctx).ErrorContext(ctx, "search.bulk.request_failed", slog.String("err", wrapped.Error()), slog.String("index", index),
			measured.attr())
		return 0, bulkRequestError{err: wrapped, measured: measured}
	}
	accepted, err := checkBulkItems(ctx, index, response, operations, policy)
	if err != nil {
		return accepted, bulkRequestError{err: err, measured: measured}
	}
	telemetry.L(ctx).DebugContext(ctx, "search.bulk.completed", slog.String("index", index), measured.attr())
	return accepted, nil
}

// checkBulkItems matches each item result in response with its operation.
// It returns the length of the accepted prefix and the error of the first
// item that policy does not accept.
func checkBulkItems(ctx context.Context, index string, response *opensearchapi.BulkResp, operations []bulkOperation,
	policy bulkItemPolicy,
) (int, error) {
	items := make([]opensearchapi.BulkRespItem, 0, len(operations))
	for _, operation := range response.Items {
		for _, item := range []*opensearchapi.BulkRespItem{operation.Create, operation.Delete, operation.Index, operation.Update} {
			if item != nil {
				items = append(items, *item)
			}
		}
	}
	if len(items) != len(operations) {
		wrapped := fmt.Errorf("OpenSearch bulk request to %s returned %d items for %d operations", index, len(items), len(operations))
		telemetry.L(ctx).ErrorContext(ctx, "search.bulk.items_invalid", slog.String("err", wrapped.Error()), slog.String("index", index))
		return 0, wrapped
	}
	for position, operation := range operations {
		if position >= len(items) {
			break
		}
		item := items[position]
		itemID := ""
		if item.ID != nil {
			itemID = *item.ID
		}
		outcome := bulkItemOutcomeFor(item, operation.documentID, policy)
		switch outcome {
		case bulkItemAccepted:
			continue
		case bulkItemObsolete:
			obsolete := fmt.Errorf("document %s in %s has a higher generation: %w", itemID, index, searchdomain.ErrObsoleteWrite)
			telemetry.L(ctx).InfoContext(ctx, "search.bulk.item_obsolete", slog.String("err", obsolete.Error()),
				slog.String("document_id", itemID), slog.String("index", index))
			return position, obsolete
		case bulkItemConflict:
			conflict := fmt.Errorf("document %s in %s: %w", itemID, index, searchdomain.ErrConcurrentWrite)
			telemetry.L(ctx).InfoContext(ctx, "search.bulk.item_conflict", slog.String("err", conflict.Error()),
				slog.String("document_id", itemID), slog.String("index", index))
			return position, conflict
		case bulkItemRejected:
			reason := ""
			if item.Error != nil {
				reason = item.Error.Type
				if item.Error.Reason != nil {
					reason += ": " + *item.Error.Reason
				}
			}
			rejected := fmt.Errorf("bulk item %s for operation %s in %s returned status %d %s", itemID, operation.documentID, index, item.Status, reason)
			telemetry.L(ctx).ErrorContext(ctx, "search.bulk.item_failed", slog.String("err", rejected.Error()),
				slog.String("document_id", operation.documentID), slog.String("index", index), slog.Int("accepted", position))
			return position, rejected
		}
	}
	return len(operations), nil
}

// bulkItemOutcome classifies one bulk item result.
type bulkItemOutcome uint8

const (
	bulkItemAccepted bulkItemOutcome = iota + 1
	bulkItemObsolete
	bulkItemConflict
	bulkItemRejected
)

func bulkItemOutcomeFor(item opensearchapi.BulkRespItem, documentID string, policy bulkItemPolicy) bulkItemOutcome {
	switch {
	case item.ID == nil || *item.ID != documentID:
		return bulkItemRejected
	case item.Error == nil && item.Status >= http.StatusOK && item.Status < http.StatusMultipleChoices:
		return bulkItemAccepted
	case policy == bulkAccess && item.Status == http.StatusNotFound:
		return bulkItemAccepted
	case policy == bulkAccess && hasStaleGenerationMarker(item):
		return bulkItemObsolete
	case policy == bulkGuarded && item.Status == http.StatusConflict:
		return bulkItemConflict
	default:
		return bulkItemRejected
	}
}

// hasStaleGenerationMarker reports whether the item error reason, or the
// reason of one of its two nested causes, contains staleGenerationMarker.
// OpenSearch wraps a Painless exception in a script exception.
func hasStaleGenerationMarker(item opensearchapi.BulkRespItem) bool {
	for cause, depth := item.Error, 0; cause != nil && depth < 3; cause, depth = cause.CausedBy, depth+1 {
		if cause.Reason != nil && strings.Contains(*cause.Reason, staleGenerationMarker) {
			return true
		}
	}
	return false
}
