package mcp

import (
	"context"
	"net/http"
	"sync"
	"time"

	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/label"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/project"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/state"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverCacheTTL = 60 * time.Second

type cachedServer struct {
	server  *mcp.Server
	builtAt time.Time
}

// Handler builds a per-user MCP server on first request by resolving workspaces
// from the bearer token, then caches it for serverCacheTTL.
type Handler struct {
	workspaces workspace.Repository
	projects   project.Repository
	projectSvc interface {
		Create(ctx context.Context, p *project.Project) (*project.Project, error)
	}
	states     state.Repository
	labels     label.Repository
	issueSvc   *service.IssueService
	epicSvc    *service.EpicService
	cycleSvc   *service.CycleService
	moduleSvc  *service.ModuleService
	nodeTypes   node.TypeRepository
	properties  node.PropertyRepository
	activity    node.ActivityRepository
	assignments node.AssignmentRepository
	nodeLabels  node.NodeLabelRepository
	containment node.ContainmentRepository
	comments    node.CommentRepository
	orgs        org.Repository
	users       user.Repository
	searcher    domainsearch.Searcher

	mu    sync.RWMutex
	cache map[uuid.UUID]*cachedServer

	httpHandler http.Handler
}

type Deps struct {
	Workspaces workspace.Repository
	Projects   project.Repository
	ProjectSvc interface {
		Create(ctx context.Context, p *project.Project) (*project.Project, error)
	}
	States      state.Repository
	Labels      label.Repository
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
	Orgs        org.Repository
	Users       user.Repository
	Searcher    domainsearch.Searcher
}

func NewHandler(deps Deps) *Handler {
	h := &Handler{
		workspaces:  deps.Workspaces,
		projects:    deps.Projects,
		projectSvc:  deps.ProjectSvc,
		states:      deps.States,
		labels:      deps.Labels,
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
		orgs:        deps.Orgs,
		users:       deps.Users,
		searcher:    deps.Searcher,
		cache:       make(map[uuid.UUID]*cachedServer),
	}
	h.httpHandler = mcp.NewStreamableHTTPHandler(h.getServer, &mcp.StreamableHTTPOptions{Stateless: true})
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.httpHandler.ServeHTTP(w, r)
}

func (h *Handler) getServer(r *http.Request) *mcp.Server {
	ctx := r.Context()

	userID, ok := auth.UserID(ctx)
	if !ok {
		return h.buildServer(nil)
	}

	h.mu.RLock()
	if c, ok := h.cache[userID]; ok && time.Since(c.builtAt) < serverCacheTTL {
		h.mu.RUnlock()
		return c.server
	}
	h.mu.RUnlock()

	var nodeTypes []*node.NodeType
	wss, err := h.workspaces.ListForUser(ctx, userID)
	if err != nil {
		telemetry.L(ctx).Error("mcp: list workspaces for user", "err", err)
	}
	// Deduplicate by Slug: two workspaces with the same slug (e.g. "issue") produce one tool set.
	seen := make(map[string]struct{})
	for _, ws := range wss {
		nts, err := h.nodeTypes.List(ctx, ws.OrgID)
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

	s := h.buildServer(nodeTypes)

	h.mu.Lock()
	h.cache[userID] = &cachedServer{server: s, builtAt: time.Now()}
	h.mu.Unlock()

	return s
}

func (h *Handler) buildServer(nodeTypes []*node.NodeType) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "tack", Version: "0.1.0"}, nil)

	tools.RegisterWorkspace(s, h.workspaces, h.projects, h.states, h.nodeTypes, h.properties)
	tools.RegisterMembers(s, h.workspaces, h.orgs, h.users)
	tools.RegisterProject(s, h.projects, h.projectSvc, h.states)
	tools.RegisterState(s, h.states)
	tools.RegisterLabel(s, h.labels)
	tools.RegisterProperty(s, h.properties)
	tools.RegisterActivity(s, h.activity)
	tools.RegisterSearch(s, h.searcher)
	tools.RegisterComment(s, h.workspaces, h.comments)

	binding := tools.NodeTypeBinding{
		IssueSvc:    h.issueSvc,
		EpicSvc:     h.epicSvc,
		CycleSvc:    h.cycleSvc,
		ModuleSvc:   h.moduleSvc,
		Properties:  h.properties,
		Activity:    h.activity,
		Containment: h.containment,
		Resolver:    tools.NewResolver(h.workspaces, h.projects),
	}
	for _, nt := range nodeTypes {
		tools.RegisterNodeTools(s, nt, binding)
	}

	tools.RegisterMyIssues(s, h.issueSvc, binding.Resolver)

	tools.RegisterResources(s, h.workspaces, h.projects, h.states, h.nodeTypes, h.properties)
	tools.RegisterPrompts(s, nodeTypes)

	return s
}
