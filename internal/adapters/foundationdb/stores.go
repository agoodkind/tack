// Package foundationdb provides FoundationDB adapters for the generic node,
// property, and relationship stores. No concept-specific stores exist:
// assignments, labels, comments, activity, automation etc. are all expressed
// as Nodes + Relationships in the generic primitives.
package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// Stores bundles the generic FDB adapters.
type Stores struct {
	db            fdb.Database
	NodeTypes     *NodeTypeStore
	PropertyDefs  *PropertyDefStore
	Nodes         *NodeStore
	Views         *ViewStore
	Relationships *RelationshipStore
	Inspect       *InspectStore
	NodeDeleter   *NodeDeleteStore
}

// NewStores opens FDB once and wires all generic stores to the same connection.
// sqlPool is reserved for auth-adjacent queries (org_members), not domain data.
func NewStores(clusterFile string, sqlPool *pgxpool.Pool) (*Stores, error) {
	db, err := Open(clusterFile)
	if err != nil {
		return nil, err
	}
	return newStores(db, sqlPool), nil
}

func newStores(db fdb.Database, _ *pgxpool.Pool) *Stores {
	return &Stores{
		db:            db,
		NodeTypes:     NewNodeTypeStore(db),
		PropertyDefs:  NewPropertyDefStore(db),
		Nodes:         NewNodeStore(db),
		Views:         NewViewStore(db),
		Relationships: NewRelationshipStore(db),
		Inspect:       NewInspectStore(db),
		NodeDeleter:   NewNodeDeleteStore(db),
	}
}

// Ping fetches a read version to verify the FoundationDB client can serve a
// read. A context deadline bounds each FDB retry.
func (s *Stores) Ping(ctx context.Context) (err error) {
	defer telemetry.FDBOp(ctx, "store.ping")(&err)
	if contextErr := ctx.Err(); contextErr != nil {
		return fdbPingError(ctx, contextErr)
	}
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		return fdbPingError(ctx, transactionErr)
	}
	defer transaction.Cancel()

	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		timeoutMilliseconds := deadline.Sub(clock.Now()).Milliseconds()
		if timeoutMilliseconds <= 0 {
			return fdbPingError(ctx, context.DeadlineExceeded)
		}
		if timeoutErr := transaction.Options().SetTimeout(timeoutMilliseconds); timeoutErr != nil {
			return fdbPingError(ctx, timeoutErr)
		}
	}

	for {
		_, readErr := transaction.GetReadVersion().Get()
		if readErr == nil {
			return nil
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return fdbPingError(ctx, contextErr)
		}
		var fdbErr fdb.Error
		if !errors.As(readErr, &fdbErr) {
			return fdbPingError(ctx, readErr)
		}
		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			return fdbPingError(ctx, retryErr)
		}
	}
}

func fdbPingError(ctx context.Context, err error) error {
	slog.ErrorContext(ctx, "foundationdb.ping_failed", slog.String("err", err.Error()))
	return fmt.Errorf("foundationdb ping: %w", err)
}
