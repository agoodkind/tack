package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/meilisearch/meilisearch-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"goodkind.io/tack/internal/clock"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// SearchVariants runs one federated multi-search with one query per variant
// and returns the hits merged and ranked by Meilisearch, at most limit of
// them. Every variant carries the same filter, so org isolation holds for
// each one.
func (c *Client) SearchVariants(ctx context.Context, collection string, queries []string, filters map[string]string, limit int) ([]domainsearch.NodeDoc, error) {
	ctx, span := telemetry.StartSpan(ctx, "search.query_variants",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("search.collection", collection),
			attribute.Int("search.variant_count", len(queries)),
			attribute.Int("search.filter_count", len(filters)),
		),
	)
	defer span.End()

	start := clock.Now()
	filter := buildFilter(filters)
	requests := make([]*meilisearch.SearchRequest, 0, len(queries))
	for _, query := range queries {
		requests = append(requests, &meilisearch.SearchRequest{IndexUID: collection, Query: query, Filter: filter})
	}
	response, err := c.meili.MultiSearch(&meilisearch.MultiSearchRequest{
		Federation: &meilisearch.MultiSearchFederation{Limit: int64(limit)},
		Queries:    requests,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		slog.WarnContext(ctx, "search.query_variants_failed",
			slog.String("collection", collection),
			slog.Int("variant_count", len(queries)),
			slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("federated search of %d variants in %s: %w", len(queries), collection, err)
	}
	docs := decodeHits(response.Hits)
	span.SetStatus(codes.Ok, "ok")
	span.SetAttributes(
		attribute.Int("search.result_count", len(docs)),
		attribute.Int64("search.duration_ms", clock.Since(start).Milliseconds()),
	)
	telemetry.L(ctx).Debug("search.query_variants_completed",
		slog.String("collection", collection),
		slog.Int("variant_count", len(queries)),
		slog.Int("result_count", len(docs)),
		slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
	)
	return docs, nil
}

// buildFilter renders equality filters as one Meilisearch filter expression.
func buildFilter(filters map[string]string) string {
	parts := make([]string, 0, len(filters))
	for key, value := range filters {
		parts = append(parts, fmt.Sprintf(`%s = "%s"`, key, value))
	}
	return strings.Join(parts, " AND ")
}

// decodeHits turns raw hits into NodeDocs, dropping any hit that does not
// decode or that names no id.
func decodeHits(hits meilisearch.Hits) []domainsearch.NodeDoc {
	docs := make([]domainsearch.NodeDoc, 0, len(hits))
	for _, hit := range hits {
		encoded, err := json.Marshal(hit)
		if err != nil {
			continue
		}
		var doc domainsearch.NodeDoc
		if err := json.Unmarshal(encoded, &doc); err != nil {
			continue
		}
		if doc.ID == "" {
			continue
		}
		docs = append(docs, doc)
	}
	return docs
}
