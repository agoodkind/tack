package mcp

import (
	"context"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/clock"
)

// userServer returns a cached tool server for userID or builds and caches one.
// It reuses a cached server only while the server's age remains within the cache lifetime.
// When h.epochs is set, it reads the current metadata epochs on every call
// and also requires the cached epoch string to match.
func (h *Handler) userServer(ctx context.Context, span trace.Span, userID uuid.UUID, orgIDs []uuid.UUID) (*cachedServer, error) {
	epoch := ""
	if h.epochs != nil {
		read, err := h.epochs.MetadataEpoch(ctx, orgIDs)
		if err != nil {
			return nil, metadataReadFailure(ctx, "read metadata epoch", err)
		}
		epoch = read
	}
	h.mu.RLock()
	cached, ok := h.cache[userID]
	h.mu.RUnlock()
	if ok && cached.epoch == epoch && clock.Since(cached.builtAt) < serverCacheTTL {
		span.SetAttributes(attribute.Bool("mcp.server_cache_hit", true))
		return cached, nil
	}
	span.SetAttributes(attribute.Bool("mcp.server_cache_hit", false))
	nodeTypes, propertyDefs, err := h.collectMetadata(ctx, orgIDs)
	if err != nil {
		return nil, err
	}
	mcpSvr, resolver := h.buildServer(nodeTypes, propertyDefs)
	built := &cachedServer{
		mcpSvr: mcpSvr, httpSvr: mcpserver.NewStreamableHTTPServer(mcpSvr, mcpserver.WithStateLess(true)),
		resolver: resolver, nodeTypes: nodeTypes, propertyDefs: propertyDefs, builtAt: clock.Now(), epoch: epoch,
	}
	span.SetAttributes(attribute.Int("mcp.node_type_count", len(nodeTypes)))
	h.mu.Lock()
	h.cache[userID] = built
	h.mu.Unlock()
	return built, nil
}
