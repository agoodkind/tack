package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// maxSearchNameBytes limits the name prefix that search uses to boost name
// matches.
const maxSearchNameBytes = 512

// NodeContentStore reads bounded pages of node search text from
// FoundationDB.
type NodeContentStore struct {
	db       fdb.Database
	policies *searchaccess.PolicySet
}

// NewNodeContentStore creates the node content reader.
//
// The reader uses the supplied FoundationDB connection and search access
// policies.
//
//	reader := NewNodeContentStore(database, policies)
//	page, err := reader.Content(ctx, request)
func NewNodeContentStore(db fdb.Database, policies *searchaccess.PolicySet) *NodeContentStore {
	return &NodeContentStore{db: db, policies: policies}
}

// Content returns one bounded UTF-8 page of the node's current revision.
//
// It returns ErrContentChanged when the request or cursor belongs to another
// revision or projection. It returns ErrWorkChanged when the requested
// search generation or access versions differ from the stored ones. It
// returns ErrNotFound after deletion.
//
//	NextCursor: continuation for a nonfinal page.
//	Done: true when the page ends at the end of the rendered text.
func (s *NodeContentStore) Content(
	ctx context.Context,
	request searchdomain.ContentRequest,
) (page node.ContentPage, err error) {
	defer telemetry.FDBOp(ctx, "store.search_content.read")(&err)
	snapshot, err := s.readSnapshot(ctx, request.NodeID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return page, nodeContentContextError{
				operation: "read search content for node " + request.NodeID.String(),
				err:       err,
			}
		}

		return page, contentFailure(
			ctx,
			"search.content.read_failed",
			"read search content",
			request.NodeID,
			err,
		)
	}

	if err := validateContentSnapshot(request, snapshot); err != nil {
		return page, err
	}

	render, err := renderContentSnapshot(ctx, request.NodeID, snapshot)
	if err != nil {
		return page, err
	}

	revision := strconv.FormatInt(snapshot.revision, 10)

	for index := range render.Diagnostics {
		render.Diagnostics[index].Revision = revision
		render.Diagnostics[index].ProjectionVersion = snapshot.projectionID
	}

	position, err := contentPosition(ctx, request, revision, snapshot.projectionID)
	if errors.Is(err, node.ErrContentChanged) {
		return page, nodeContentContextError{
			operation: "validate content cursor for node " + request.NodeID.String(),
			err:       err,
		}
	}

	if err != nil {
		return page, contentFailure(
			ctx,
			"search.content.cursor_invalid",
			"validate content cursor",
			request.NodeID,
			err,
		)
	}

	access, err := compileSearchAccess(
		ctx,
		s.policies,
		snapshot.access.Versions,
		snapshot.node.OrgID,
		request.NodeID,
		snapshot.generation,
	)
	if err != nil {
		return page, nodeContentContextError{
			operation: "compile search access for node " + request.NodeID.String(),
			err:       err,
		}
	}

	identity := contentIdentity{
		nodeID:     request.NodeID,
		nodeType:   snapshot.node.NodeType,
		revision:   revision,
		projection: snapshot.projectionID,
		name:       boundedSearchName(snapshot.node.Name),
	}
	page, err = contentPage(ctx, render.Text, identity, access, position)
	if err != nil {
		return node.ContentPage{}, contentFailure(
			ctx,
			"search.content.page_invalid",
			"encode content page",
			request.NodeID,
			err,
		)
	}

	page.Diagnostics = render.Diagnostics

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

func contentFailure(
	ctx context.Context,
	event string,
	operation string,
	nodeID uuid.UUID,
	err error,
) error {
	if searchFailureWasLogged(err) {
		return err
	}

	wrapped := fmt.Errorf("%s for node %s: %w", operation, nodeID, err)
	telemetry.L(ctx).ErrorContext(
		ctx,
		event,
		slog.String("err", wrapped.Error()),
		slog.String("node_id", nodeID.String()),
	)

	return loggedSearchError{err: wrapped}
}
