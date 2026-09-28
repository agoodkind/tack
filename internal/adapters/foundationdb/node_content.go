package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// maxSearchNameBytes bounds the name prefix stored for lexical boosting.
const maxSearchNameBytes = 512

// NodeContentStore reads bounded pages of node search text from FoundationDB.
type NodeContentStore struct {
	db       fdb.Database
	policies *searchaccess.PolicySet
}

// NewNodeContentStore creates the node content reader.
func NewNodeContentStore(db fdb.Database, policies *searchaccess.PolicySet) *NodeContentStore {
	return &NodeContentStore{db: db, policies: policies}
}

// Content returns one bounded UTF-8 page of the node's current revision. It
// returns ErrContentChanged when the request or cursor belongs to another
// revision or projection. It returns ErrWorkChanged when the requested search
// generation or access versions differ from the stored ones. It returns
// ErrNotFound after deletion.
func (s *NodeContentStore) Content(ctx context.Context, request searchdomain.ContentRequest) (page node.ContentPage, err error) {
	defer telemetry.FDBOp(ctx, "store.search_content.read")(&err)
	snapshot, err := s.readSnapshot(ctx, request.NodeID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return page, nodeContentContextError{operation: "read search content for node " + request.NodeID.String(), err: err}
		}
		return page, contentFailure(ctx, "search.content.read_failed", "read search content", request.NodeID, err)
	}
	if snapshot.revision <= 0 || request.ProjectionConfig != "" && request.ProjectionConfig != snapshot.projectionID {
		return page, nodeContentContextError{operation: "validate revision and projection for node " + request.NodeID.String(), err: node.ErrContentChanged}
	}
	if request.SearchGeneration > 0 && request.SearchGeneration != snapshot.generation {
		return page, searchdomain.ErrWorkChanged
	}
	if len(request.AccessVersions) > 0 && !slices.Equal(request.AccessVersions, snapshot.access.Versions) {
		return page, searchdomain.ErrWorkChanged
	}
	text, err := node.EmitSearchText(ctx, snapshot.node.Name, snapshot.node.Props, snapshot.definitions)
	if err != nil {
		return page, nodeContentContextError{operation: "emit search text for node " + request.NodeID.String(), err: err}
	}
	revision := strconv.FormatInt(snapshot.revision, 10)
	position, err := contentPosition(ctx, request, revision, snapshot.projectionID)
	if errors.Is(err, node.ErrContentChanged) {
		return page, nodeContentContextError{operation: "validate content cursor for node " + request.NodeID.String(), err: err}
	}
	if err != nil {
		return page, contentFailure(ctx, "search.content.cursor_invalid", "validate content cursor", request.NodeID, err)
	}
	access, err := compileSearchAccess(ctx, s.policies, snapshot.access.Versions, snapshot.node.OrgID, request.NodeID, snapshot.generation)
	if err != nil {
		return page, nodeContentContextError{operation: "compile search access for node " + request.NodeID.String(), err: err}
	}
	identity := contentIdentity{
		nodeID: request.NodeID, nodeType: snapshot.node.NodeType, revision: revision,
		projection: snapshot.projectionID, name: boundedSearchName(snapshot.node.Name),
	}
	page, err = contentPage(ctx, text, identity, access, position)
	if err != nil {
		return node.ContentPage{}, contentFailure(ctx, "search.content.page_invalid", "encode content page", request.NodeID, err)
	}
	return page, nil
}

// contentIdentity stores the node, type, revision, projection, and name that
// every page of one read shares.
type contentIdentity struct {
	nodeID     uuid.UUID
	nodeType   string
	revision   string
	projection string
	name       string
}

func boundedSearchName(name string) string {
	if len(name) <= maxSearchNameBytes {
		return name
	}
	end := maxSearchNameBytes
	for end > 0 && !utf8.RuneStart(name[end]) {
		end--
	}
	return name[:end]
}

type nodeContentContextError struct {
	operation string
	err       error
}

func (e nodeContentContextError) Error() string { return e.operation + ": " + e.err.Error() }
func (e nodeContentContextError) Unwrap() error { return e.err }

func contentFailure(ctx context.Context, event, operation string, nodeID uuid.UUID, err error) error {
	if searchFailureWasLogged(err) {
		return err
	}
	wrapped := fmt.Errorf("%s for node %s: %w", operation, nodeID, err)
	telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
	return loggedSearchError{err: wrapped}
}
