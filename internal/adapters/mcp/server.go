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

// Handler builds a per-user MCP server on first request by resolving workspaces
// from the bearer token, then caches it for serverCacheTTL.
type Handler struct {
	projectSvc *service.ProjectService
	entities    node.EntityRepository
	reader      node.NodeReader
	issueSvc    *service.IssueService
	epicSvc     *service.EpicService
	cycleSvc    *service.CycleService
	moduleSvc   *service.ModuleService
	nodeTypes   node.TypeRepository
	properties  node.PropertyRepository
	activity    node.ActivityRepository
	assignments node.AssignmentRepository
	nodeLabels  node.NodeLabelRepository
	containment node.ContainmentRepository
	comments    node.CommentRepository
	members     org.MemberRepository
	users       user.Repository
	searcher    domainsearch.Searcher

	mu    sync.RWMutex
	cache map[uuid.UUID]*cachedServer
}

type Deps struct {
	ProjectSvc *service.ProjectService
	Entities    node.EntityRepository
	Reader      node.NodeReader
	IssueSvc    *service.IssueService
	EpicSvc     *service.EpicService
	CycleSvc    *service.CycleService
	ModuleSvc   *service.ModuleService
	NodeTypes   node.TypeRepository
	Properties  node.PropertyRepository
	Activity    node.ActivityRepository
	Assignments node.AssignmentRepository
	NodeLabels  node.NodeLabelRepository
	Containment node.ContainmentRepository
	Comments    node.CommentRepository
	Members     org.MemberRepository
	Users       user.Repository
	Searcher    domainsearch.Searcher
}

func NewHandler(deps Deps) *Handler {
	return &Handler{
		projectSvc:  deps.ProjectSvc,
		entities:    deps.Entities,
		reader:      deps.Reader,
		issueSvc:    deps.IssueSvc,
		epicSvc:     deps.EpicSvc,
		cycleSvc:    deps.CycleSvc,
		moduleSvc:   deps.ModuleSvc,
		nodeTypes:   deps.NodeTypes,
		properties:  deps.Properties,
		activity:    deps.Activity,
		assignments: deps.Assignments,
		nodeLabels:  deps.NodeLabels,
		containment: deps.Containment,
		comments:    deps.Comments,
		members:     deps.Members,
		users:       deps.Users,
		searcher:    deps.Searcher,
		cache:       make(map[uuid.UUID]*cachedServer),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID, ok := auth.UserID(ctx)
	if !ok {
		// auth middleware should have blocked this, but be safe
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

	// build new server for this user
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

	resolver := tools.NewResolver(h.entities, h.reader, h.members)

	tools.RegisterWorkspace(s, h.entities, h.reader, h.nodeTypes, h.properties, resolver)
	tools.RegisterMembers(s, h.members, h.users, resolver)
	tools.RegisterProject(s, h.projectSvc, h.entities, h.reader, resolver)
	tools.RegisterProperty(s, h.properties)
	tools.RegisterActivity(s, h.activity, resolver)
	tools.RegisterSearch(s, h.searcher, resolver)
	tools.RegisterComment(s, h.comments, resolver)

	binding := tools.NodeTypeBinding{
		IssueSvc:    h.issueSvc,
		EpicSvc:     h.epicSvc,
		CycleSvc:    h.cycleSvc,
		ModuleSvc:   h.moduleSvc,
		Properties:  h.properties,
		Activity:    h.activity,
		Containment: h.containment,
		Entities:    h.entities,
		Reader:      h.reader,
		Resolver:    resolver,
	}
	for _, nt := range nodeTypes {
		tools.RegisterNodeTools(s, nt, binding)
	}

	tools.RegisterMyIssues(s, h.issueSvc, binding.Resolver)

	tools.RegisterResources(s, h.reader, h.nodeTypes, h.properties, resolver)
	tools.RegisterPrompts(s, nodeTypes)

	return s
}
