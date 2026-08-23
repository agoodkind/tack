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
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

const serverCacheTTL = 60 * time.Second

type cachedServer struct {
	mcpSvr  *mcpserver.MCPServer
	httpSvr *mcpserver.StreamableHTTPServer
	builtAt time.Time
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
	searcher      domainsearch.Searcher

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
	Searcher      domainsearch.Searcher
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
		searcher:      d.Searcher,
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
	r = r.WithContext(ctx)
	r, err := withMCPRequestMetadata(r)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "read_request_body_failed")
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	span.SetAttributes(attribute.String("enduser.id", userID.String()))

	// The membership set travels on every request, cache hit or not. The
	// resolvers refuse any node outside it, and the tool wrapper refuses any
	// result that never passed a membership check, so a failed lookup fails
	// the request closed instead of serving with an empty set.
	orgIDs, err := h.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "list_org_ids_failed")
		log.Error("mcp: list org ids", "err", err)
		http.Error(w, `{"error":"membership unavailable"}`, http.StatusInternalServerError)
		return
	}
	ctx = auth.WithOrgMembership(ctx, orgIDs)
	r = r.WithContext(ctx)

	h.mu.RLock()
	c, ok := h.cache[userID]
	h.mu.RUnlock()
	if ok && clock.Since(c.builtAt) < serverCacheTTL {
		span.SetAttributes(attribute.Bool("mcp.server_cache_hit", true))
		c.httpSvr.ServeHTTP(w, r)
		return
	}
	span.SetAttributes(attribute.Bool("mcp.server_cache_hit", false))

	nodeTypes, propertyDefs := h.collectMetadata(ctx, span, orgIDs)
	mcpSvr := h.buildServer(nodeTypes, propertyDefs)
	httpSvr := mcpserver.NewStreamableHTTPServer(mcpSvr, mcpserver.WithStateLess(true))
	span.SetAttributes(attribute.Int("mcp.node_type_count", len(nodeTypes)))

	h.mu.Lock()
	h.cache[userID] = &cachedServer{mcpSvr: mcpSvr, httpSvr: httpSvr, builtAt: clock.Now()}
	h.mu.Unlock()
	httpSvr.ServeHTTP(w, r)
}

// collectMetadata gathers the node types and property definitions of every
// org the user belongs to, deduplicated by slug and name, for the per-user
// server build.
func (h *Handler) collectMetadata(ctx context.Context, span trace.Span, orgIDs []uuid.UUID) ([]*node.NodeType, []*node.PropertyDef) {
	log := telemetry.L(ctx)
	var nodeTypes []*node.NodeType
	seen := make(map[string]struct{})
	var propertyDefs []*node.PropertyDef
	seenPropertyDefs := make(map[string]struct{})
	for _, orgID := range orgIDs {
		nts, err := h.nodeTypes.List(ctx, orgID)
		if err != nil {
			span.RecordError(err)
			log.ErrorContext(ctx, "mcp: node type list", "org_id", orgID, "err", err)
			continue
		}
		for _, nt := range nts {
			if _, dup := seen[nt.Slug]; !dup {
				seen[nt.Slug] = struct{}{}
				nodeTypes = append(nodeTypes, nt)
			}
		}
		defs, err := h.propertyDefs.List(ctx, orgID)
		if err != nil {
			span.RecordError(err)
			log.ErrorContext(ctx, "mcp: property def list", "org_id", orgID, "err", err)
			continue
		}
		for _, def := range defs {
			if _, dup := seenPropertyDefs[def.Name]; !dup {
				seenPropertyDefs[def.Name] = struct{}{}
				propertyDefs = append(propertyDefs, def)
			}
		}
	}
	return nodeTypes, propertyDefs
}

func (h *Handler) buildServer(nodeTypes []*node.NodeType, propertyDefs []*node.PropertyDef) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("tack", "0.2.0")

	resolver := tools.NewResolver(h.nodes, h.reader, h.members, nodeTypes)

	tools.RegisterWorkspace(s, h.reader, resolver, nodeTypes)
	tools.RegisterMembers(s, h.members, h.users, resolver)
	tools.RegisterProperty(s, h.propertyDefs, resolver)
	tools.RegisterSearch(s, h.searcher, resolver)
	tools.RegisterRelationship(s, h.nodeSvc, h.relationships, resolver)

	binding := tools.NodeTypeBinding{
		NodeSvc:      h.nodeSvc,
		Reader:       h.reader,
		PropertyDefs: h.propertyDefs,
		Resolver:     resolver,
		Users:        h.users,
	}
	for _, nt := range nodeTypes {
		tools.RegisterNodeTools(s, nt, binding)
	}
	tools.RegisterReferencePropertyTools(s, binding, nodeTypes, propertyDefs)

	tools.RegisterResources(s, h.reader, resolver, nodeTypes, propertyDefs)
	tools.RegisterPrompts(s, resolver, nodeTypes, propertyDefs)
	return s
}
