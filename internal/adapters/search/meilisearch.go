package search

import (
	"context"
	"encoding/json"
	"errors"
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

// Client is a Meilisearch-backed Searcher. It implements domain/search.Searcher.
type Client struct {
	meili meilisearch.ServiceManager
}

// New creates a Meilisearch Client connecting to url using masterKey.
func New(url, masterKey string) *Client {
	return &Client{
		meili: meilisearch.New(url, meilisearch.WithAPIKey(masterKey)),
	}
}

// EnsureIndex creates the named collection if it does not exist and sets the
// given fields as filterable attributes. Safe to call on every startup (idempotent).
func (c *Client) EnsureIndex(collection string, filterableAttributes []string) error {
	_, err := c.meili.CreateIndex(&meilisearch.IndexConfig{
		Uid:        collection,
		PrimaryKey: "id",
	})
	if err != nil {
		var meiliErr *meilisearch.Error
		if !errors.As(err, &meiliErr) || meiliErr.MeilisearchApiError.Code != "index_already_exists" {
			return fmt.Errorf("create index %s: %w", collection, err)
		}
	}
	attrs := make([]interface{}, len(filterableAttributes))
	for i, a := range filterableAttributes {
		attrs[i] = a
	}
	if _, err := c.meili.Index(collection).UpdateFilterableAttributes(&attrs); err != nil {
		return fmt.Errorf("set filterable attributes on %s: %w", collection, err)
	}
	return nil
}

// Index adds or replaces doc in collection, using "id" as the primary key.
func (c *Client) Index(ctx context.Context, collection, _ string, doc *domainsearch.NodeDoc) error {
	ctx, span := telemetry.StartSpan(ctx, "search.index",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("search.collection", collection),
			attribute.String("search.document_id", doc.ID),
		),
	)
	defer span.End()

	start := clock.Now()
	pk := "id"
	if _, err := c.meili.Index(collection).AddDocuments([]any{doc}, &meilisearch.DocumentOptions{PrimaryKey: &pk}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Warn("search.index_failed",
			slog.String("collection", collection),
			slog.String("document_id", doc.ID),
			slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("index document in %s: %w", collection, err)
	}
	span.SetStatus(codes.Ok, "ok")
	span.SetAttributes(attribute.Int64("search.duration_ms", clock.Since(start).Milliseconds()))
	telemetry.L(ctx).Debug("search.indexed",
		slog.String("collection", collection),
		slog.String("document_id", doc.ID),
		slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
	)
	return nil
}

// Delete removes the document with the given id from collection.
func (c *Client) Delete(ctx context.Context, collection, id string) error {
	ctx, span := telemetry.StartSpan(ctx, "search.delete",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("search.collection", collection),
			attribute.String("search.document_id", id),
		),
	)
	defer span.End()

	start := clock.Now()
	if _, err := c.meili.Index(collection).DeleteDocument(id, nil); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Warn("search.delete_failed",
			slog.String("collection", collection),
			slog.String("document_id", id),
			slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("delete document %s from %s: %w", id, collection, err)
	}
	span.SetStatus(codes.Ok, "ok")
	span.SetAttributes(attribute.Int64("search.duration_ms", clock.Since(start).Milliseconds()))
	telemetry.L(ctx).Debug("search.deleted",
		slog.String("collection", collection),
		slog.String("document_id", id),
		slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
	)
	return nil
}

// Search returns NodeDocs matching query, scoped by equality filters, plus
// facet counts on every filterable attribute the index has configured.
//
// Facet field choice. We could hardcode a small list (org_id, node_type)
// here, but that drifts from EnsureIndex when callers add filterable
// attributes. Instead the search reads the live filterableAttributes off
// the index and asks Meilisearch to bucket on all of them. That keeps
// search results aligned with whatever the index actually allows.
//
// Returns a non-nil empty slice when the search succeeded but matched nothing.
// All NodeDoc fields are populated from the indexed document; no FDB reads needed.
func (c *Client) Search(ctx context.Context, collection, query string, filters map[string]string) ([]domainsearch.NodeDoc, map[string]map[string]int64, error) {
	ctx, span := telemetry.StartSpan(ctx, "search.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("search.collection", collection),
			attribute.Int("search.filter_count", len(filters)),
		),
	)
	defer span.End()

	start := clock.Now()
	filterParts := make([]string, 0, len(filters))
	for k, v := range filters {
		filterParts = append(filterParts, fmt.Sprintf(`%s = "%s"`, k, v))
	}

	rawFacets, err := c.meili.Index(collection).GetFilterableAttributes()
	var facetFields []string
	if err == nil && rawFacets != nil {
		for _, v := range *rawFacets {
			if s, ok := v.(string); ok {
				facetFields = append(facetFields, s)
			}
		}
	}
	res, err := c.meili.Index(collection).Search(query, &meilisearch.SearchRequest{
		Filter: strings.Join(filterParts, " AND "),
		Limit:  200,
		Facets: facetFields,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		telemetry.L(ctx).Warn("search.query_failed",
			slog.String("collection", collection),
			slog.Int("filter_count", len(filters)),
			slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
			slog.String("err", err.Error()),
		)
		return nil, nil, fmt.Errorf("search %s: %w", collection, err)
	}

	docs := make([]domainsearch.NodeDoc, 0, len(res.Hits))
	for _, hit := range res.Hits {
		b, err := json.Marshal(hit)
		if err != nil {
			continue
		}
		var doc domainsearch.NodeDoc
		if err := json.Unmarshal(b, &doc); err != nil {
			continue
		}
		if doc.ID == "" {
			continue
		}
		docs = append(docs, doc)
	}

	facets := parseFacets(res.FacetDistribution)
	span.SetStatus(codes.Ok, "ok")
	span.SetAttributes(
		attribute.Int("search.result_count", len(docs)),
		attribute.Int("search.facet_field_count", len(facetFields)),
		attribute.Int64("search.duration_ms", clock.Since(start).Milliseconds()),
	)
	telemetry.L(ctx).Debug("search.query_completed",
		slog.String("collection", collection),
		slog.Int("filter_count", len(filters)),
		slog.Int("result_count", len(docs)),
		slog.Int64("duration_ms", clock.Since(start).Milliseconds()),
	)
	return docs, facets, nil
}

// parseFacets decodes Meilisearch's json.RawMessage facet distribution into
// map[string]map[string]int64 for clean consumption by callers.
func parseFacets(raw json.RawMessage) map[string]map[string]int64 {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]map[string]float64
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded) == 0 {
		return nil
	}
	out := make(map[string]map[string]int64, len(decoded))
	for field, vals := range decoded {
		counts := make(map[string]int64, len(vals))
		for v, c := range vals {
			counts[v] = int64(c)
		}
		out[field] = counts
	}
	return out
}
