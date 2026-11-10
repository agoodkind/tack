package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

const serverCacheTTL = 60 * time.Second

// MetadataEpochReader returns the metadata epoch values for the specified
// organizations as one string. Reads of the same organization set return
// equal strings only when each organization's epoch remains unchanged.
type MetadataEpochReader interface {
	MetadataEpoch(ctx context.Context, orgIDs []uuid.UUID) (string, error)
}

type cachedServer struct {
	mcpSvr       *mcpserver.MCPServer
	httpSvr      *mcpserver.StreamableHTTPServer
	resolver     *tools.Resolver
	nodeTypes    []*node.NodeType
	propertyDefs []*node.PropertyDef
	builtAt      time.Time
	epoch        string
}

// Handler serves MCP HTTP requests and caches a server for each user. Each
// server uses the node types available to that user.
type Handler struct {
	nodeSvc            *service.NodeService
	nodes              node.NodeRepository
	reader             node.NodeReader
	nodeTypes          node.TypeRepository
	propertyDefs       node.PropertyDefRepository
	relationships      node.RelationshipRepository
	relationshipLister node.RelationshipBatchLister
	members            org.MemberRepository
	users              user.Repository
	search             tools.SearchBinding
	epochs             MetadataEpochReader
	recorder           audit.Recorder

	mu    sync.RWMutex
	cache map[uuid.UUID]*cachedServer
}

// Deps supplies the dependencies for the MCP handler.
type Deps struct {
	NodeSvc            *service.NodeService
	Nodes              node.NodeRepository
	Reader             node.NodeReader
	NodeTypes          node.TypeRepository
	PropertyDefs       node.PropertyDefRepository
	Relationships      node.RelationshipRepository
	RelationshipLister node.RelationshipBatchLister
	Members            org.MemberRepository
	Users              user.Repository
	// Search runs ranked search for tack_search. A binding without a runner
	// returns the fixed unavailable response.
	Search tools.SearchBinding
	// Epochs reads the metadata epochs that decide whether a cached tool server
	// still matches stored metadata. With a nil reader, the cache checks only
	// the server lifetime.
	Epochs MetadataEpochReader
	// AuditRecorder is the audit recorder of the runtime graph that owns this
	// handler. The tool wrapper and the resource handlers record their events
	// with it. With a nil recorder, the handler refuses each state-changing
	// tool call and each resource read that must record an event.
	AuditRecorder audit.Recorder
}

func NewHandler(d Deps) *Handler {
	return &Handler{
		nodeSvc:            d.NodeSvc,
		nodes:              d.Nodes,
		reader:             d.Reader,
		nodeTypes:          d.NodeTypes,
		propertyDefs:       d.PropertyDefs,
		relationships:      d.Relationships,
		relationshipLister: d.RelationshipLister,
		members:            d.Members,
		users:              d.Users,
		search:             d.Search,
		epochs:             d.Epochs,
		recorder:           d.AuditRecorder,
		cache:              make(map[uuid.UUID]*cachedServer),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.StartSpan(
		r.Context(),
		"mcp.http.request",
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

	// The request context stores the JSON-RPC request ID, session ID, and
	// membership set. Create idempotency uses the request ID when a session ID
	// exists, the arguments do not supply a nonempty string for
	// idempotency_key, and explicit operation metadata does not supply an ID.
	// Preserve these values when deriving the handler context.
	//
	// Before TACK-476, ServeHTTP rebuilt the request context from a context
	// without the parsed transport identity. That discarded the identity that
	// create idempotency needs to recognize a retried create and reuse its
	// node.
	ctx = tools.WithMCPRequestMetadata(ctx, metadata)
	ctx = tools.WithAuditRecorder(ctx, h.recorder)
	span.SetAttributes(attribute.String("enduser.id", userID.String()))

	// Every request obtains membership, including requests that reuse a cached
	// server. The handler reuses membership from authentication or queries
	// membership when the context does not contain it. A failed lookup refuses
	// the request. callerOrgIDs uses attached membership for node checks;
	// Workspace and WorkspacesForUser query the repository directly. The tool
	// wrapper refuses successful results when the handler did not mark
	// authorization.
	//
	// TACK-503 removed the MCP handler's repeated membership query when
	// authentication had already attached its membership result. Before that
	// change, the handler queried membership again even after the
	// authentication lookup.
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

	c, err := h.userServer(ctx, span, userID, orgIDs)
	if err != nil {
		metadataUnavailable(w, span, err)
		return
	}

	addMetadataTargets(ctx, c)
	c.httpSvr.ServeHTTP(w, r)
}
