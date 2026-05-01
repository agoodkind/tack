package mcp

import (
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
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
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
	auditReader   *audit.Reader
	auditRedactor *audit.Redactor

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
	AuditReader   *audit.Reader
	AuditRedactor *audit.Redactor
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
		auditReader:   d.AuditReader,
		auditRedactor: d.AuditRedactor,
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
	span.SetAttributes(attribute.String("enduser.id", userID.String()))

	h.mu.RLock()
	c, ok := h.cache[userID]
	h.mu.RUnlock()
	if ok && time.Since(c.builtAt) < serverCacheTTL {
		span.SetAttributes(attribute.Bool("mcp.server_cache_hit", true))
		c.httpSvr.ServeHTTP(w, r)
		return
	}
	span.SetAttributes(attribute.Bool("mcp.server_cache_hit", false))

	orgIDs, err := h.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "list_org_ids_failed")
		log.Error("mcp: list org ids", "err", err)
	}
	var nodeTypes []*node.NodeType
	seen := make(map[string]struct{})
	for _, orgID := range orgIDs {
		nts, err := h.nodeTypes.List(ctx, orgID)
		if err != nil {
			span.RecordError(err)
			log.Error("mcp: node type list", "org_id", orgID, "err", err)
			continue
		}
		for _, nt := range nts {
			if _, dup := seen[nt.Slug]; !dup {
				seen[nt.Slug] = struct{}{}
				nodeTypes = append(nodeTypes, nt)
			}
		}
	}

	mcpSvr := h.buildServer(nodeTypes)
	httpSvr := mcpserver.NewStreamableHTTPServer(mcpSvr, mcpserver.WithStateLess(true))
	span.SetAttributes(attribute.Int("mcp.node_type_count", len(nodeTypes)))

	h.mu.Lock()
	h.cache[userID] = &cachedServer{mcpSvr: mcpSvr, httpSvr: httpSvr, builtAt: time.Now()}
	h.mu.Unlock()
	httpSvr.ServeHTTP(w, r)
}

func (h *Handler) buildServer(nodeTypes []*node.NodeType) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("tack", "0.2.0")

	resolver := tools.NewResolver(h.nodes, h.reader, h.members, nodeTypes)

	tools.RegisterWorkspace(s, h.reader, resolver, nodeTypes)
	tools.RegisterMembers(s, h.members, h.users, resolver)
	tools.RegisterProperty(s, h.propertyDefs, resolver)
	tools.RegisterSearch(s, h.searcher, resolver)
	tools.RegisterRelationship(s, h.nodeSvc, h.relationships, resolver)
	tools.RegisterAudit(s, h.auditReader, h.auditRedactor, resolver)

	binding := tools.NodeTypeBinding{
		NodeSvc:  h.nodeSvc,
		Reader:   h.reader,
		Resolver: resolver,
		Users:    h.users,
	}
	for _, nt := range nodeTypes {
		tools.RegisterNodeTools(s, nt, binding)
	}

	tools.RegisterResources(s, h.reader, resolver, nodeTypes)
	tools.RegisterPrompts(s, resolver, nodeTypes)
	return s
}
