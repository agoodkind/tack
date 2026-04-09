package mcp

import (
	"net/http"
	"sync"
	"time"

	"github.com/agoodkind/tack/internal/adapters/mcp/tools"
	"github.com/agoodkind/tack/internal/auth"
	"github.com/agoodkind/tack/internal/domain/cycle"
	"github.com/agoodkind/tack/internal/domain/epic"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/agoodkind/tack/internal/domain/label"
	"github.com/agoodkind/tack/internal/domain/module"
	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/agoodkind/tack/internal/domain/project"
	"github.com/agoodkind/tack/internal/domain/state"
	"github.com/agoodkind/tack/internal/domain/workspace"
	"github.com/agoodkind/tack/internal/telemetry"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverCacheTTL = 60 * time.Second

type cachedServer struct {
	server  *mcp.Server
	builtAt time.Time
}

// Handler is the MCP HTTP handler. It builds a per-user MCP server on first
// request by resolving workspaces from the bearer token, then caches it for
// serverCacheTTL. Custom node types defined in FDB generate additional tools.
type Handler struct {
	workspaces workspace.Repository
	projects   project.Repository
	states     state.Repository
	labels     label.Repository
	issueSvc   issue.Service
	epics      epic.Repository
	cycles     cycle.Repository
	modules    module.Repository
	nodeTypes  node.TypeRepository
	properties node.PropertyRepository
	activity   node.ActivityRepository

	mu    sync.RWMutex
	cache map[uuid.UUID]*cachedServer // keyed by user ID

	httpHandler http.Handler
}

type Deps struct {
	Workspaces workspace.Repository
	Projects   project.Repository
	States     state.Repository
	Labels     label.Repository
	IssueSvc   issue.Service
	Epics      epic.Repository
	Cycles     cycle.Repository
	Modules    module.Repository
	NodeTypes  node.TypeRepository
	Properties node.PropertyRepository
	Activity   node.ActivityRepository
}

func NewHandler(deps Deps) *Handler {
	h := &Handler{
		workspaces: deps.Workspaces,
		projects:   deps.Projects,
		states:     deps.States,
		labels:     deps.Labels,
		issueSvc:   deps.IssueSvc,
		epics:      deps.Epics,
		cycles:     deps.Cycles,
		modules:    deps.Modules,
		nodeTypes:  deps.NodeTypes,
		properties: deps.Properties,
		activity:   deps.Activity,
		cache:      make(map[uuid.UUID]*cachedServer),
	}
	h.httpHandler = mcp.NewStreamableHTTPHandler(h.getServer, &mcp.StreamableHTTPOptions{Stateless: true})
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.httpHandler.ServeHTTP(w, r)
}

// getServer resolves the authenticated user from the request context, fetches
// all their workspaces, collects all custom node types across those workspaces,
// and builds a per-user MCP server. Cached by user ID for serverCacheTTL.
func (h *Handler) getServer(r *http.Request) *mcp.Server {
	ctx := r.Context()

	userID, ok := auth.UserID(ctx)
	if !ok {
		// No authenticated user — return static-only tools.
		return h.buildServer(nil)
	}

	// Check cache
	h.mu.RLock()
	if c, ok := h.cache[userID]; ok && time.Since(c.builtAt) < serverCacheTTL {
		h.mu.RUnlock()
		return c.server
	}
	h.mu.RUnlock()

	// Resolve all workspaces this user has access to, then collect
	// all custom node types across their orgs (deduplicated by type ID).
	var nodeTypes []*node.NodeType
	wss, err := h.workspaces.ListForUser(ctx, userID)
	if err != nil {
		telemetry.L(ctx).Error("mcp: list workspaces for user", "err", err)
	}

	seen := make(map[uuid.UUID]struct{})
	for _, ws := range wss {
		nts, err := h.nodeTypes.List(ctx, ws.OrgID)
		if err != nil {
			continue
		}
		for _, nt := range nts {
			if _, dup := seen[nt.ID]; !dup {
				seen[nt.ID] = struct{}{}
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

	// ── Discovery ─────────────────────────────────────────────────────────────
	tools.RegisterWorkspace(s, h.workspaces, h.projects, h.states, h.nodeTypes, h.properties)

	// ── Projects ──────────────────────────────────────────────────────────────
	tools.RegisterProject(s, h.projects, h.states)

	// ── States & Labels ───────────────────────────────────────────────────────
	tools.RegisterState(s, h.states)
	tools.RegisterLabel(s, h.labels)

	// ── Core PM entities ──────────────────────────────────────────────────────
	tools.RegisterIssue(s, h.issueSvc)
	tools.RegisterEpic(s, h.epics, h.projects)
	tools.RegisterCycle(s, h.cycles)
	tools.RegisterModule(s, h.modules)

	// ── Extensibility ─────────────────────────────────────────────────────────
	tools.RegisterProperty(s, h.properties)
	tools.RegisterActivity(s, h.activity)
	tools.RegisterSearch(s, h.issueSvc)

	// ── Dynamic tools per custom node type ────────────────────────────────────
	for _, nt := range nodeTypes {
		tools.RegisterNodeType(s, nt, h.properties, h.activity)
	}

	return s
}
