package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// NodeStore implements node.NodeRepository using FoundationDB. When
// searchWork is true, each node write schedules search work stamped by the
// clock in the same transaction.
type NodeStore struct {
	db         fdb.Database
	clock      clock.Clock
	searchWork bool
}

// NewNodeStore creates the node store. It schedules no search work until
// [Stores.EnableSearchWork] runs.
func NewNodeStore(db fdb.Database, source clock.Clock) *NodeStore {
	return &NodeStore{db: db, clock: source, searchWork: false}
}

// Get resolves the node by ID through its global resolution record.
func (s *NodeStore) Get(ctx context.Context, orgID, nodeID uuid.UUID) (n *node.Node, err error) {
	defer telemetry.FDBOp(ctx, "store.node.get")(&err)
	var encoded []byte
	err = runNodeReadTransaction(ctx, s.db, "node.get", func(tr fdb.Transaction) error {
		resolveBytes, readErr := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
		if readErr != nil || len(resolveBytes) == 0 {
			return readErr
		}
		var resolve node.NodeResolve
		if decodeErr := json.Unmarshal(resolveBytes, &resolve); decodeErr != nil {
			return fmt.Errorf("unmarshal resolve: %w", decodeErr)
		}
		if resolve.OrgID != orgID {
			return nil
		}
		encoded, readErr = tr.Get(fdb.Key(nodeInstanceKey(orgID, resolve.NodeType, nodeID))).Get()
		return readErr
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return nil, err
		}
		wrapped := fmt.Errorf("fdb get node %s: %w", nodeID, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.get_failed", slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
		return nil, wrapped
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	var current node.Node
	if err := json.Unmarshal(encoded, &current); err != nil {
		wrapped := fmt.Errorf("unmarshal node %s: %w", nodeID, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.decode_failed", slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
		return nil, wrapped
	}
	return &current, nil
}

// Set stores a node and its view. When search work is enabled, the same
// transaction also schedules search work for the node.
func (s *NodeStore) Set(ctx context.Context, current *node.Node, view *node.NodeView) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.set")(&err)
	transactionErr := runNodeMutation(ctx, s.db, "node.set", func(tr fdb.Transaction) error {
		if err := writeNodeRecords(ctx, tr, current, view); err != nil {
			return err
		}
		if !s.searchWork {
			return nil
		}
		_, scheduleErr := scheduleSearchChange(ctx, tr, s.clock.Now(), current.OrgID, current.ID, searchChangeContent)
		return scheduleErr
	})
	if transactionErr != nil {
		if searchFailureWasLogged(transactionErr) {
			return transactionErr
		}
		err = fmt.Errorf("set node %s: %w", current.ID, transactionErr)
		if !searchFailureWasLogged(transactionErr) {
			telemetry.L(ctx).ErrorContext(ctx, "node.set_failed", slog.String("err", err.Error()), slog.String("node_id", current.ID.String()))
		}
	}
	return err
}
