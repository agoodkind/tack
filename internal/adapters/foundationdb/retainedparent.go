package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/telemetry"
)

// storedParentID reads the primary record of source inside transaction and
// returns the
// parent_id that the record stores. It returns [uuid.Nil] without an error
// when
// source does not identify a node in the organization, the primary record
// does not exist, or the record does not store parent_id. It returns an
// error
// when the stored parent_id is not a JSON string that contains a UUID.
func storedParentID(
	ctx context.Context,
	transaction fdb.Transaction,
	orgID uuid.UUID,
	source hierarchyNode,
) (uuid.UUID, error) {
	if !source.Found {
		return uuid.Nil, nil
	}

	current, _, _, err := readNodeAndView(ctx, transaction, orgID, source)
	if err != nil || current == nil {
		return uuid.Nil, err
	}

	return parentIDValue(ctx, source.ID, current.Props[parentIDProperty])
}

// parentIDValue decodes raw as a JSON string and parses that string as a
// UUID.
// It returns [uuid.Nil] without an error when raw is empty. It returns an
// error
// that matches domain.ErrFailedPrecondition when raw does not encode a JSON
// string or the string does not contain a UUID.
func parentIDValue(ctx context.Context, nodeID uuid.UUID, raw json.RawMessage) (uuid.UUID, error) {
	if len(raw) == 0 {
		return uuid.Nil, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return uuid.Nil, invalidStoredParent(ctx, nodeID, raw, err)
	}

	parentID, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil, invalidStoredParent(ctx, nodeID, raw, err)
	}

	return parentID, nil
}

// invalidStoredParent builds an error for the parent_id value raw on nodeID
// that wraps cause and domain.ErrFailedPrecondition. It logs the error once
// at Error level with the event relationship.remove_refused and the
// source_id
// attribute set to nodeID. It returns the error as an already logged error.
func invalidStoredParent(
	ctx context.Context,
	nodeID uuid.UUID,
	raw json.RawMessage,
	cause error,
) error {
	wrapped := fmt.Errorf(
		"node %s stores a parent_id value %s that is not a UUID string: %w: %w",
		nodeID,
		raw,
		cause,
		domain.ErrFailedPrecondition,
	)
	telemetry.L(ctx).ErrorContext(
		ctx,
		"relationship.remove_refused",
		slog.String("err", wrapped.Error()),
		slog.String("source_id", nodeID.String()),
	)

	return loggedSearchError{err: wrapped}
}
