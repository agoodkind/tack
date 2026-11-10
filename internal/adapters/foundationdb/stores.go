// Package foundationdb provides FoundationDB adapters for generic node,
// property, and relationship stores. Assignments, labels, comments,
// activity, automation, and other concepts use generic nodes and
// relationships rather than separate stores.
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

// Stores groups the generic FoundationDB adapters.
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

// NewStores opens FoundationDB once and gives all generic stores the same
// connection.
//
// A positive transactionTimeout sets the shared database transaction
// timeout; see [Open]. sqlPool remains reserved for auth queries such as
// org_members and must not store domain data. This constructor does not use
// sqlPool.
//
//   - A connection error prevents construction of the stores.
//   - A zero or negative transactionTimeout leaves the database timeout
//     option unset.
func NewStores(
	clusterFile string,
	transactionTimeout time.Duration,
	sqlPool *pgxpool.Pool,
) (*Stores, error) {
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

// UseClock sets the clock that timestamps search work from node,
// relationship, property definition, and node type writes.
//
// The runtime graph sets its clock once before the stores serve any request.
//
//   - Existing search work retains its saved enqueue time.
func (s *Stores) UseClock(source clock.Clock) {
	s.NodeTypes.clock = source
	s.PropertyDefs.clock = source
	s.Nodes.clock = source
	s.Relationships.clock = source
}

// EnableSearchWork turns on search work scheduling in node, relationship,
// property definition, and node type writes.
//
// Callers enable it only when OPENSEARCH_ENDPOINT is set. Without it, those
// writes do not store search work, search access, fanout, or permission
// event keys.
//
//   - Enabling the flag does not schedule work for existing records.
func (s *Stores) EnableSearchWork() {
	s.NodeTypes.searchWork = true
	s.PropertyDefs.searchWork = true
	s.Nodes.searchWork = true
	s.Relationships.searchWork = true
}

// SearchContent constructs a content reader on the shared FoundationDB
// connection.
//
// The reader uses the supplied policies to compile access for search pages.
//
//   - A continuation detects a changed revision or projection.
//   - A positive request.SearchGeneration that differs from the stored
//     generation returns ErrWorkChanged.
func (s *Stores) SearchContent(policies *searchaccess.PolicySet) *NodeContentStore {
	return NewNodeContentStore(s.db, policies)
}

// SearchWork constructs a durable search work store on the shared
// FoundationDB connection.
//
// The supplied clock sets and checks work leases and initializes claim
// rotation. An unexpired claim blocks another owner only while its
// generation matches the pending work and its target and mirror match the
// current claim scope. After expiry, a later owner can resume from a stored
// checkpoint under these rules.
//
//   - A stored live checkpoint resets when its revision differs from the
//     pending work record.
//   - Other stored non-rescan checkpoints reset when their generation
//     differs from the pending work record.
//   - Rescan work reads its scan state, which survives newer rescan
//     requests.
func (s *Stores) SearchWork(source clock.Clock) *SearchWorkStore {
	return NewSearchWorkStore(s.db, source)
}

// SearchAccess constructs an access-state store on the shared FoundationDB
// connection.
//
// The store uses the supplied policies to compile access for nodes. It reads
// the access state that search work maintains.
//
//   - Compile uses the authority rollout's current write versions.
//   - A deleted node uses recorded counterparts for dependent queries.
func (s *Stores) SearchAccess(policies *searchaccess.PolicySet) *SearchAccessStateStore {
	return NewSearchAccessStateStore(s.db, policies)
}

// SearchPolicySet constructs the production access policy set.
//
// The policy set reads node types through the type-key index and
// relationships in bounded pages. It uses the generic view, node type, and
// relationship stores.
//
//   - The stable organization policy uses StableVersion.
//   - The rotated key namespace uses RotatedVersion without changing the
//     access decision.
func (s *Stores) SearchPolicySet() *searchaccess.PolicySet {
	return searchaccess.NewPolicySet(s.Views, s.NodeTypes, s.Relationships)
}

var _ node.NodeReader = (*ViewStore)(nil)

// Ping fetches a read version to verify that the FoundationDB client can
// serve a read.
//
// A context deadline bounds each FoundationDB retry. The function checks
// cancellation before creating the transaction and after a failed read.
//
//   - A successful read version returns nil.
//   - A nonretryable read error returns a foundationdb ping error.
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
