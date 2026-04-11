//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"goodkind.io/tack/internal/domain/node"
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// CommentStore implements node.CommentRepository using FoundationDB.
// Keys are ordered by timestamp so range reads return comments chronologically.
type CommentStore struct {
	db fdb.Database
}

func NewCommentStore(db fdb.Database) *CommentStore {
	return &CommentStore{db: db}
}

func (s *CommentStore) Create(_ context.Context, orgID uuid.UUID, comment *node.Comment) error {
	b, err := json.Marshal(comment)
	if err != nil {
		return fmt.Errorf("marshal comment: %w", err)
	}
	key := tuple.Tuple{
		keyCommentOnNode,
		orgID.String(),
		comment.NodeID.String(),
		comment.CreatedAt.UnixNano(),
		comment.ID.String(),
	}.Pack()
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(key), b)
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("fdb create comment: %w", err)
	}
	return nil
}

func (s *CommentStore) List(_ context.Context, orgID, nodeID uuid.UUID) ([]*node.Comment, error) {
	prefix := tuple.Tuple{keyCommentOnNode, orgID.String(), nodeID.String()}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb list comments: %w", err)
	}
	kvs := vals.([]fdb.KeyValue)
	comments := make([]*node.Comment, 0, len(kvs))
	for _, kv := range kvs {
		var c node.Comment
		if err := json.Unmarshal(kv.Value, &c); err != nil {
			return nil, fmt.Errorf("unmarshal comment: %w", err)
		}
		comments = append(comments, &c)
	}
	return comments, nil
}
