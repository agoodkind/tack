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
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
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
	// OpsOutbox reads and clears operator-command audit events.
	OpsOutbox *OpsOutboxStore
}

// NewStores opens FDB once and wires all generic stores to the same connection.
// sqlPool is reserved for auth-adjacent queries (org_members), not domain data.
// transactionTimeout bounds every transaction these stores run; see [Open].
func NewStores(clusterFile string, transactionTimeout time.Duration, sqlPool *pgxpool.Pool) (*Stores, error) {
	db, err := Open(clusterFile, transactionTimeout)
	if err != nil {
		return nil, err
	}
	return newStores(db, sqlPool), nil
}

func newStores(db fdb.Database, _ *pgxpool.Pool) *Stores {
	source := processClock{}
	nodes := NewNodeStore(db, source)
	return &Stores{
		db:            db,
		NodeTypes:     NewNodeTypeStore(db, source),
		PropertyDefs:  NewPropertyDefStore(db, source),
		Nodes:         nodes,
		Views:         NewViewStore(db),
		Relationships: NewRelationshipStore(db, source),
		Inspect:       NewInspectStore(db),
		NodeDeleter:   NewNodeDeleteStore(nodes),
		OpsOutbox:     NewOpsOutboxStore(db),
	}
}

// processClock reads the process-wide clock through the clock package
// helpers. Stores use it until UseClock installs an injected clock.
type processClock struct{}

func (processClock) Now() time.Time { return clock.Now() }

func (processClock) Since(start time.Time) time.Duration { return clock.Since(start) }

// UseClock installs the injected clock that stamps the search work every
// source write schedules. The runtime graph installs its clock once, before
// the stores serve any request.
func (s *Stores) UseClock(source clock.Clock) {
	s.NodeTypes.clock = source
	s.PropertyDefs.clock = source
	s.Nodes.clock = source
	s.Relationships.clock = source
}

// EnableSearchWork turns on search work scheduling in node, relationship,
// property definition, and node type writes. Callers turn it on only when
// OPENSEARCH_ENDPOINT is set. Without it, those writes store no search work,
// search access, fanout, or permission event key.
func (s *Stores) EnableSearchWork() {
	s.NodeTypes.searchWork = true
	s.PropertyDefs.searchWork = true
	s.Nodes.searchWork = true
	s.Relationships.searchWork = true
}

// SearchContent constructs a content reader on the shared FoundationDB connection.
func (s *Stores) SearchContent(policies *searchaccess.PolicySet) *NodeContentStore {
	return NewNodeContentStore(s.db, policies)
}

// SearchWork constructs a durable search work store on the shared connection.
func (s *Stores) SearchWork(source clock.Clock) *SearchWorkStore {
	return NewSearchWorkStore(s.db, source)
}

// SearchAccess constructs an access-state store on the shared connection.
func (s *Stores) SearchAccess(policies *searchaccess.PolicySet) *SearchAccessStateStore {
	return NewSearchAccessStateStore(s.db, policies)
}

// SearchPolicySet constructs the registered production access policy. It
// reads node types through the type-key index and relationships in bounded
// pages.
func (s *Stores) SearchPolicySet() *searchaccess.PolicySet {
	return searchaccess.NewPolicySet(s.Views, s.NodeTypes, s.Relationships)
}

var _ node.NodeReader = (*ViewStore)(nil)

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
