package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

const serverCacheTTL = 60 * time.Second

// MetadataEpochReader returns the metadata epoch values for the specified
// organizations as one string. Two reads for the same organizations return
// equal strings only when no organization's epoch changed between them.
type MetadataEpochReader interface {
	MetadataEpoch(ctx context.Context, orgIDs []uuid.UUID) (string, error)
}

// cachedServer is one user's generated tool server and the metadata epoch
// it was built from.
type cachedServer struct {
	mcpSvr  *mcpserver.MCPServer
	httpSvr *mcpserver.StreamableHTTPServer
	builtAt time.Time
	epoch   string
}

// Handler is the MCP HTTP entry point. It caches a per-user MCP server that
// knows about the user's accessible NodeTypes.
type Handler struct {
	nodeSvc       *service.NodeService
	nodes         node.NodeRepository
	reader        node.NodeReader
	nodeTypes     node.TypeRepository
	propertyDefs  node.PropertyDefRepository
	relationships node.RelationshipRepository
	members       org.MemberRepository
	users         user.Repository
	search        tools.SearchBinding
	epochs        MetadataEpochReader

	mu    sync.RWMutex
	cache map[uuid.UUID]*cachedServer
}

// Deps bundles the dependencies for the MCP handler.
type Deps struct {
	NodeSvc       *service.NodeService
	Nodes         node.NodeRepository
	Reader        node.NodeReader
	NodeTypes     node.TypeRepository
	PropertyDefs  node.PropertyDefRepository
	Relationships node.RelationshipRepository
	Members       org.MemberRepository
	Users         user.Repository
	// Search runs ranked search for tack_search. A binding without a runner
	// keeps the fixed unavailable response.
	Search tools.SearchBinding
	// Epochs reads the metadata epochs that decide whether a cached tool
	// server still matches stored metadata. With a nil reader, the cache
	// checks only the server lifetime.
	Epochs MetadataEpochReader
}

func NewHandler(d Deps) *Handler {
	return &Handler{
		nodeSvc:       d.NodeSvc,
		nodes:         d.Nodes,
		reader:        d.Reader,
		nodeTypes:     d.NodeTypes,
		propertyDefs:  d.PropertyDefs,
		relationships: d.Relationships,
		members:       d.Members,
		users:         d.Users,
		search:        d.Search,
		epochs:        d.Epochs,
		cache:         make(map[uuid.UUID]*cachedServer),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.StartSpan(r.Context(), "mcp.http.request",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("http.route", "/mcp"),
			attribute.String("http.request.method", r.Method),
		),
	)
	defer span.End()

	log := telemetry.L(ctx)

	userID, ok := auth.UserID(ctx)
	if !ok {
		span.SetStatus(codes.Error, "unauthenticated")
		http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
		return
	}
	ctx = telemetry.WithTraceLogger(ctx, slog.String("user_id", userID.String()))
	metadata, err := readMCPRequestMetadata(r)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "read_request_body_failed")
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	// One context chain carries the JSON-RPC id, the session id, and the
	// membership set into the MCP server. A create's idempotency key reads
	// the first two, so a context rebuilt without them lets a retried create
	// write a second node (TACK-476).
	ctx = tools.WithMCPRequestMetadata(ctx, metadata)
	span.SetAttributes(attribute.String("enduser.id", userID.String()))

	// The membership set is on every request, cache hit or not. The
	// resolvers refuse any node outside it, and the tool wrapper refuses any
	// result that never passed a membership check. The auth middleware
	// attaches the set it read for the auth event, so a request normally
	// costs one membership read (TACK-503); when the middleware's read
	// failed and attached nothing, the lookup runs here and a failure fails
	// the request closed instead of serving with an empty set.
	orgIDs, attached := auth.OrgMembership(ctx)
	if !attached {
		orgIDs, err = h.members.ListOrgIDsForUser(ctx, userID)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "list_org_ids_failed")
			log.Error("mcp: list org ids", "err", err)
			http.Error(w, `{"error":"membership unavailable"}`, http.StatusInternalServerError)
			return
		}
		ctx = auth.WithOrgMembership(ctx, orgIDs)
	}
	r = r.WithContext(ctx)

	// With an epoch reader, the epoch read runs before dispatch on every
	// request, and a cached server serves only while it matches the current
	// epochs of every organization. Without one, the cache checks only its
	// lifetime.
	epoch := ""
	if h.epochs != nil {
		epoch, err = h.epochs.MetadataEpoch(ctx, orgIDs)
		if err != nil {
			metadataUnavailable(w, span, metadataReadFailure(ctx, "read metadata epoch", err))
			return
		}
	}
	h.mu.RLock()
	c, ok := h.cache[userID]
	h.mu.RUnlock()
	if ok && c.epoch == epoch && clock.Since(c.builtAt) < serverCacheTTL {
		span.SetAttributes(attribute.Bool("mcp.server_cache_hit", true))
		c.httpSvr.ServeHTTP(w, r)
		return
	}
	span.SetAttributes(attribute.Bool("mcp.server_cache_hit", false))

	nodeTypes, propertyDefs, err := h.collectMetadata(ctx, orgIDs)
	if err != nil {
		metadataUnavailable(w, span, err)
		return
	}
	mcpSvr := h.buildServer(nodeTypes, propertyDefs)
	httpSvr := mcpserver.NewStreamableHTTPServer(mcpSvr, mcpserver.WithStateLess(true))
	span.SetAttributes(attribute.Int("mcp.node_type_count", len(nodeTypes)))

	h.mu.Lock()
	h.cache[userID] = &cachedServer{mcpSvr: mcpSvr, httpSvr: httpSvr, builtAt: clock.Now(), epoch: epoch}
	h.mu.Unlock()
	httpSvr.ServeHTTP(w, r)
}
