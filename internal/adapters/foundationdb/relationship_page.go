package foundationdb

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// maxRelationshipPage bounds one relationship page.
const maxRelationshipPage = 100

// EdgesFrom reads at most limit relationships of every type from sourceID
// after cursor and returns their target node IDs in key order.
func (s *RelationshipStore) EdgesFrom(ctx context.Context, orgID, sourceID uuid.UUID, cursor string, limit int) (page node.IDPage, err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.edges_from")(&err)
	return s.edgePage(ctx, relationshipPrefixBySource(orgID, sourceID, ""), sourceID, cursor, limit)
}

// EdgesTo reads at most limit relationships of every type to targetID after
// cursor and returns their source node IDs in key order.
func (s *RelationshipStore) EdgesTo(ctx context.Context, orgID, targetID uuid.UUID, cursor string, limit int) (page node.IDPage, err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.edges_to")(&err)
	return s.edgePage(ctx, relationshipReversePrefixByTarget(orgID, targetID, ""), targetID, cursor, limit)
}

// edgePage reads one bounded page of the relationship keys under prefix.
// The last tuple element of each key is the node at the other end. The
// cursor is the last key the previous page read.
func (s *RelationshipStore) edgePage(ctx context.Context, prefix []byte, nodeID uuid.UUID, cursor string, limit int) (node.IDPage, error) {
	if limit < 1 || limit > maxRelationshipPage {
		wrapped := fmt.Errorf("list edges of node %s: limit must be between 1 and %d", nodeID, maxRelationshipPage)
		telemetry.L(ctx).ErrorContext(ctx, "store.relationship.page_invalid", slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
		return node.IDPage{}, loggedSearchError{err: wrapped}
	}
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return node.IDPage{}, nodeOperationFailure(ctx, "create relationship range for node "+nodeID.String(), err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return node.IDPage{}, nodeOperationFailure(ctx, "decode relationship cursor for node "+nodeID.String(), decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	var items []fdb.KeyValue
	err = runNodeReadTransaction(ctx, s.db, "relationship.edge_page", func(tr fdb.Transaction) error {
		var readErr error
		items, readErr = tr.GetRange(selection, fdb.RangeOptions{Limit: limit + 1}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read relationship page of node "+nodeID.String(), readErr)
		}
		return nil
	})
	if err != nil {
		return node.IDPage{}, err
	}
	done := len(items) <= limit
	if !done {
		items = items[:limit]
	}
	page := node.IDPage{IDs: make([]uuid.UUID, 0, len(items)), NextCursor: "", Done: done}
	for _, item := range items {
		otherID, decodeErr := edgeEnd(ctx, item.Key)
		if decodeErr != nil {
			return node.IDPage{}, decodeErr
		}
		page.IDs = append(page.IDs, otherID)
	}
	if !done && len(items) > 0 {
		page.NextCursor = base64.RawURLEncoding.EncodeToString(items[len(items)-1].Key)
	}
	return page, nil
}

// edgeEnd decodes the node at the other end of one forward or reverse
// relationship key. That node is the fifth tuple element.
func edgeEnd(ctx context.Context, key fdb.Key) (uuid.UUID, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return uuid.Nil, searchReadFailure(ctx, "unpack relationship key", err)
	}
	if len(values) < 5 {
		return uuid.Nil, searchReadFailure(ctx, "decode relationship key", fmt.Errorf("relationship key has %d tuple elements", len(values)))
	}
	text, isText := values[4].(string)
	if !isText {
		return uuid.Nil, searchReadFailure(ctx, "decode relationship key", fmt.Errorf("relationship key end is %T, not a string", values[4]))
	}
	identifier, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil, searchReadFailure(ctx, "parse relationship key end", err)
	}
	return identifier, nil
}
