package mcp

import (
	"net/http"
	"sync"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
)

const serverCacheTTL = 60 * time.Second

type cachedServer struct {
	mcpSvr  *mcpserver.MCPServer
	httpSvr *mcpserver.StreamableHTTPServer
	builtAt time.Time
}

type Handler struct {
	nodeSvc    *service.NodeService
	entities   node.EntityRepository
	reader     node.NodeReader
	nodeTypes  node.TypeRepository
	properties node.PropertyRepository
	comments   node.CommentRepository
	members    org.MemberRepository
	users      user.Repository
	searcher   domainsearch.Searcher

	mu    sync.RWMutex
	cache map[uuid.UUID]*cachedServer
}

type Deps struct {
	NodeSvc    *service.NodeService
	Entities   node.EntityRepository
	Reader     node.NodeReader
	NodeTypes  node.TypeRepository
	Properties node.PropertyRepository
	Comments   node.CommentRepository
	Members    org.MemberRepository
	Users      user.Repository
	Searcher   domainsearch.Searcher
}

func NewHandler(deps Deps) *Handler {
	return &Handler{
		nodeSvc:    deps.NodeSvc,
		entities:   deps.Entities,
		reader:     deps.Reader,
		nodeTypes:  deps.NodeTypes,
		properties: deps.Properties,
		comments:   deps.Comments,
		members:    deps.Members,
		users:      deps.Users,
		searcher:   deps.Searcher,
		cache:      make(map[uuid.UUID]*cachedServer),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := auth.UserID(ctx)
	if !ok {
		http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
		return
	}

	h.mu.RLock()
	c, ok := h.cache[userID]
	h.mu.RUnlock()
	if ok && time.Since(c.builtAt) < serverCacheTTL {
		c.httpSvr.ServeHTTP(w, r)
		return
	}

	var nodeTypes []*node.NodeType
	orgIDs, err := h.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		telemetry.L(ctx).Error("mcp: list org ids for user", "err", err)
	}
	seen := make(map[string]struct{})
	for _, orgID := range orgIDs {
		nts, err := h.nodeTypes.List(ctx, orgID)
		if err != nil {
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

	h.mu.Lock()
	h.cache[userID] = &cachedServer{mcpSvr: mcpSvr, httpSvr: httpSvr, builtAt: time.Now()}
	h.mu.Unlock()

	httpSvr.ServeHTTP(w, r)
}

func (h *Handler) buildServer(nodeTypes []*node.NodeType) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("tack", "0.1.0")

	resolver := tools.NewResolver(h.entities, h.reader, h.members, nodeTypes)

	tools.RegisterWorkspace(s, h.reader, resolver, nodeTypes)
	tools.RegisterMembers(s, h.members, h.users, resolver)
	tools.RegisterProperty(s, h.properties)
	tools.RegisterSearch(s, h.searcher, resolver)
	tools.RegisterComment(s, h.comments, resolver)

	typeIndex := node.BuildTypeIndex(nodeTypes)
	binding := tools.NodeTypeBinding{
		NodeSvc:   h.nodeSvc,
		Reader:    h.reader,
		Resolver:  resolver,
		TypeIndex: typeIndex,
	}
	for _, nt := range nodeTypes {
		tools.RegisterNodeTools(s, nt, binding)
	}

	tools.RegisterMyWork(s, h.reader, resolver)

	tools.RegisterResources(s, h.reader, resolver, nodeTypes)
	tools.RegisterPrompts(s, nodeTypes)

	return s
}
