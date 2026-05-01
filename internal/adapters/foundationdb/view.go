package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// ViewStore implements node.NodeReader using the node_view materialized records.
// All reads go through node_view keys; the service layer does not read the
// primary node_instance records directly.
type ViewStore struct {
	db fdb.Database
}

func NewViewStore(db fdb.Database) *ViewStore {
	return &ViewStore{db: db}
}

// Get resolves the node by ID via the global resolve record, then fetches its
// materialized view. No org context required from the caller.
func (s *ViewStore) Get(ctx context.Context, nodeID uuid.UUID) (view *node.NodeView, err error) {
	defer telemetry.FDBOp(ctx, "store.view.get")(&err)
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		resolveBytes, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
		if err != nil || len(resolveBytes) == 0 {
			return nil, err
		}
		var resolve node.NodeResolve
		if err := json.Unmarshal(resolveBytes, &resolve); err != nil {
			return nil, fmt.Errorf("unmarshal resolve: %w", err)
		}
		return tr.Get(fdb.Key(nodeViewKey(resolve.OrgID, resolve.NodeType, nodeID))).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("view get: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, nil
	}
	var v node.NodeView
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("unmarshal view: %w", err)
	}
	return &v, nil
}

// Resolve returns the org / type context for any node UUID.
func (s *ViewStore) Resolve(ctx context.Context, nodeID uuid.UUID) (r *node.NodeResolve, err error) {
	defer telemetry.FDBOp(ctx, "store.view.resolve")(&err)
	key := fdb.Key(nodeResolveKey(nodeID))
	var result *node.NodeResolve
	_, err = s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		b := tr.Get(key).MustGet()
		if len(b) == 0 {
			return nil, domain.ErrNotFound
		}
		var r node.NodeResolve
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("unmarshal resolve: %w", err)
		}
		result = &r
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// List scans the view key space for (OrgID, NodeType) and applies PropFilters
// in-memory. Primary indexed scans (ByProperty, BySourceRelation, ByTargetRelation)
// narrow the candidate set before the view fetch.
func (s *ViewStore) List(ctx context.Context, q node.NodeListQuery) (out []*node.NodeView, err error) {
	defer telemetry.FDBOp(ctx, "store.view.list")(&err)
	views, err := s.scanCandidates(ctx, q)
	if err != nil {
		return nil, err
	}
	// Post-scan filters.
	out = views[:0]
	for _, v := range views {
		if !matchPropFilters(v, q.PropFilters) {
			continue
		}
		if q.CreatedAfter != nil && v.CreatedAt.Before(*q.CreatedAfter) {
			continue
		}
		if q.CreatedBefore != nil && v.CreatedAt.After(*q.CreatedBefore) {
			continue
		}
		out = append(out, v)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}

// Stream is List with a channel. For now it materializes the full list and
// pumps it on a goroutine; batched/chunked streaming can be layered on later
// when callers need it.
func (s *ViewStore) Stream(ctx context.Context, q node.NodeListQuery) (<-chan node.NodeStreamResult, error) {
	ch := make(chan node.NodeStreamResult, 64)
	go func() {
		defer close(ch)
		ctx, span := telemetry.StartSpan(ctx, "store.view.stream",
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(
				attribute.String("org.id", q.OrgID.String()),
				attribute.String("node.type", q.NodeType),
			),
		)
		defer span.End()
		ctx = telemetry.WithTraceLogger(ctx,
			slog.String("org_id", q.OrgID.String()),
			slog.String("node_type", q.NodeType),
		)
		views, err := s.List(ctx, q)
		if err != nil {
			ch <- node.NodeStreamResult{Err: err}
			return
		}
		for _, v := range views {
			select {
			case <-ctx.Done():
				return
			case ch <- node.NodeStreamResult{View: v}:
			}
		}
	}()
	return ch, nil
}

func (s *ViewStore) scanCandidates(_ context.Context, q node.NodeListQuery) ([]*node.NodeView, error) {
	// Primary scan strategies in order of preference.
	switch {
	case q.ByProperty != nil:
		return s.scanByProperty(q.OrgID, q.NodeType, q.ByProperty.PropName, q.ByProperty.Value)
	case q.BySourceRelation != nil:
		return s.scanByRelation(q.OrgID, q.NodeType, q.BySourceRelation, true)
	case q.ByTargetRelation != nil:
		return s.scanByRelation(q.OrgID, q.NodeType, q.ByTargetRelation, false)
	}
	// Default: full range scan over (orgID, nodeType).
	prefix := nodeViewPrefix(q.OrgID, q.NodeType)
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb view range scan: %w", err)
	}
	return decodeViews(vals.([]fdb.KeyValue)), nil
}

func (s *ViewStore) scanByProperty(orgID uuid.UUID, nodeType, propName string, value json.RawMessage) ([]*node.NodeView, error) {
	pr, err := fdb.PrefixRange(nodeByPropertyValuePrefix(orgID, nodeType, propName, encodePropertyValue(value)))
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		indexKVs, err := tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		if err != nil {
			return nil, err
		}
		views := make([]*node.NodeView, 0, len(indexKVs))
		for _, kv := range indexKVs {
			t, terr := tuple.Unpack(stripPrefix(kv.Key))
			if terr != nil || len(t) < 6 {
				continue
			}
			nodeIDStr, _ := t[5].(string)
			id, perr := uuid.Parse(nodeIDStr)
			if perr != nil {
				continue
			}
			b, err := tr.Get(fdb.Key(nodeViewKey(orgID, nodeType, id))).Get()
			if err != nil || len(b) == 0 {
				continue
			}
			var v node.NodeView
			if err := json.Unmarshal(b, &v); err != nil {
				continue
			}
			views = append(views, &v)
		}
		return views, nil
	})
	if err != nil {
		return nil, fmt.Errorf("fdb scan by property: %w", err)
	}
	return vals.([]*node.NodeView), nil
}

func (s *ViewStore) scanByRelation(orgID uuid.UUID, nodeType string, scan *node.RelationScan, fromSource bool) ([]*node.NodeView, error) {
	var prefix []byte
	var peerIdx int
	if fromSource {
		prefix = relationshipPrefixBySource(orgID, scan.NodeID, scan.RelationType)
		peerIdx = 4 // targetID
	} else {
		prefix = relationshipReversePrefixByTarget(orgID, scan.NodeID, scan.RelationType)
		peerIdx = 4 // sourceID
	}
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		kvs, err := tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		if err != nil {
			return nil, err
		}
		views := make([]*node.NodeView, 0, len(kvs))
		for _, kv := range kvs {
			t, terr := tuple.Unpack(stripPrefix(kv.Key))
			if terr != nil || len(t) <= peerIdx {
				continue
			}
			peerStr, _ := t[peerIdx].(string)
			peerID, perr := uuid.Parse(peerStr)
			if perr != nil {
				continue
			}
			// Look up the peer via node_resolve since we don't know its NodeType.
			rb, err := tr.Get(fdb.Key(nodeResolveKey(peerID))).Get()
			if err != nil || len(rb) == 0 {
				continue
			}
			var r node.NodeResolve
			if err := json.Unmarshal(rb, &r); err != nil {
				continue
			}
			if nodeType != "" && r.NodeType != nodeType {
				continue
			}
			b, err := tr.Get(fdb.Key(nodeViewKey(r.OrgID, r.NodeType, peerID))).Get()
			if err != nil || len(b) == 0 {
				continue
			}
			var v node.NodeView
			if err := json.Unmarshal(b, &v); err != nil {
				continue
			}
			views = append(views, &v)
		}
		return views, nil
	})
	if err != nil {
		return nil, fmt.Errorf("fdb scan by relation: %w", err)
	}
	return vals.([]*node.NodeView), nil
}

// matchPropFilters returns true when every PropFilter matches on v.Props.
func matchPropFilters(v *node.NodeView, filters []node.PropertyMatch) bool {
	for _, f := range filters {
		got, ok := v.Props[f.PropName]
		if !ok {
			return false
		}
		if !bytes.Equal(got, f.Value) {
			return false
		}
	}
	return true
}

func decodeViews(kvs []fdb.KeyValue) []*node.NodeView {
	views := make([]*node.NodeView, 0, len(kvs))
	for _, kv := range kvs {
		var v node.NodeView
		if err := json.Unmarshal(kv.Value, &v); err != nil {
			continue
		}
		views = append(views, &v)
	}
	return views
}
