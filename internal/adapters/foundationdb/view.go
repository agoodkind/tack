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

// ViewStore implements node.NodeReader with materialized node_view records.
// Resolution reads node_resolve records, and indexed scans select candidates
// before fetching views. The service layer must not read primary
// node_instance records directly.
type ViewStore struct {
	db fdb.Database
}

func NewViewStore(db fdb.Database) *ViewStore {
	return &ViewStore{db: db}
}

// Get resolves the node ID through the global resolution record, then
// fetches its materialized view.
//
// The caller does not supply organization context. The resolution record
// supplies the organization and node type for the view key.
//
//   - An absent resolution record returns a nil view.
//   - An absent view record also returns a nil view.
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
		return nil, fmt.Errorf("view get: %w", classifyStorageTimeout(err))
	}

	encodedView, ok := val.([]byte)
	if !ok || len(encodedView) == 0 {
		return nil, nil
	}

	var v node.NodeView
	if err := json.Unmarshal(encodedView, &v); err != nil {
		return nil, fmt.Errorf("unmarshal view: %w", err)
	}

	return &v, nil
}

// Resolve returns the organization and node type for a node UUID.
//
// It reads the global node_resolve record. The caller does not need an
// organization or node type to select that record.
//
//   - An absent record returns [domain.ErrNotFound].
func (s *ViewStore) Resolve(
	ctx context.Context,
	nodeID uuid.UUID,
) (r *node.NodeResolve, err error) {
	defer telemetry.FDBOp(ctx, "store.view.resolve")(&err)
	key := fdb.Key(nodeResolveKey(nodeID))
	var result *node.NodeResolve
	_, err = s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		encodedResolve := tr.Get(key).MustGet()
		if len(encodedResolve) == 0 {
			return nil, domain.ErrNotFound
		}

		var r node.NodeResolve
		if err := json.Unmarshal(encodedResolve, &r); err != nil {
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

// List selects views for OrgID and NodeType and applies PropFilters in
// memory.
//
// A primary indexed scan narrows the candidates before the view fetch. The
// result also applies creation time filters and stops at a positive limit.
//
//   - ByProperty selects the property index first.
//   - BySourceRelation or ByTargetRelation selects a relationship index.
//   - A query without those selectors scans the view range.
func (s *ViewStore) List(
	ctx context.Context,
	q node.NodeListQuery,
) (out []*node.NodeView, err error) {
	defer telemetry.FDBOp(ctx, "store.view.list")(&err)
	views, err := s.scanCandidates(ctx, q)
	if err != nil {
		return nil, err
	}

	// The following checks filter the scanned views.
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

// Stream returns List results through a channel from a goroutine.
//
// It materializes the full list before sending results. Callers that need
// batched or chunked streaming require a different implementation.
//
//   - A List error becomes a channel result with Err set.
//   - Context cancellation stops the goroutine while it sends views.
func (s *ViewStore) Stream(
	ctx context.Context,
	q node.NodeListQuery,
) (<-chan node.NodeStreamResult, error) {
	ch := make(chan node.NodeStreamResult, 64)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "store.view.stream.panic", slog.Any("err", r))
			}
		}()
		defer close(ch)
		ctx, span := telemetry.StartSpan(
			ctx,
			"store.view.stream",
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(
				attribute.String("org.id", q.OrgID.String()),
				attribute.String("node.type", q.NodeType),
			),
		)
		defer span.End()
		ctx = telemetry.WithTraceLogger(
			ctx,
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

func (s *ViewStore) scanCandidates(
	_ context.Context,
	q node.NodeListQuery,
) ([]*node.NodeView, error) {
	// The switch selects the first matching primary scan strategy.
	switch {
	case q.ByProperty != nil:
		return s.scanByProperty(q.OrgID, q.NodeType, q.ByProperty.PropName, q.ByProperty.Value)
	case q.BySourceRelation != nil:
		return s.scanByRelation(q.OrgID, q.NodeType, q.BySourceRelation, true)
	case q.ByTargetRelation != nil:
		return s.scanByRelation(q.OrgID, q.NodeType, q.ByTargetRelation, false)
	}

	// The default scan reads the full range for orgID and nodeType.
	prefix := nodeViewPrefix(q.OrgID, q.NodeType)
	prefixRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}

	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(prefixRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb view range scan: %w", err)
	}

	return decodeViews(vals.([]fdb.KeyValue)), nil
}

func (s *ViewStore) scanByProperty(
	orgID uuid.UUID,
	nodeType string,
	propName string,
	value json.RawMessage,
) ([]*node.NodeView, error) {
	prefixRange, err := fdb.PrefixRange(
		nodeByPropertyValuePrefix(orgID, nodeType, propName, encodePropertyValue(value)),
	)
	if err != nil {
		return nil, err
	}

	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		indexKVs, err := tr.GetRange(prefixRange, fdb.RangeOptions{}).GetSliceWithError()
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

			encodedView, err := tr.Get(fdb.Key(nodeViewKey(orgID, nodeType, id))).Get()
			if err != nil || len(encodedView) == 0 {
				continue
			}

			var v node.NodeView
			if err := json.Unmarshal(encodedView, &v); err != nil {
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

func (s *ViewStore) scanByRelation(
	orgID uuid.UUID,
	nodeType string,
	scan *node.RelationScan,
	fromSource bool,
) ([]*node.NodeView, error) {
	var prefix []byte
	var peerIdx int
	if fromSource {
		prefix = relationshipPrefixBySource(orgID, scan.NodeID, scan.RelationType)
		peerIdx = 4 // The peer is targetID.
	} else {
		prefix = relationshipReversePrefixByTarget(orgID, scan.NodeID, scan.RelationType)
		peerIdx = 4 // The peer is sourceID.
	}

	prefixRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}

	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		kvs, err := tr.GetRange(prefixRange, fdb.RangeOptions{}).GetSliceWithError()
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

			// The node_resolve record supplies the peer's NodeType.
			encodedResolve, err := tr.Get(fdb.Key(nodeResolveKey(peerID))).Get()
			if err != nil || len(encodedResolve) == 0 {
				continue
			}

			var r node.NodeResolve
			if err := json.Unmarshal(encodedResolve, &r); err != nil {
				continue
			}

			if nodeType != "" && r.NodeType != nodeType {
				continue
			}

			encodedView, err := tr.Get(fdb.Key(nodeViewKey(r.OrgID, r.NodeType, peerID))).Get()
			if err != nil || len(encodedView) == 0 {
				continue
			}

			var v node.NodeView
			if err := json.Unmarshal(encodedView, &v); err != nil {
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
	for _, filter := range filters {
		got, ok := v.Props[filter.PropName]
		if !ok {
			return false
		}

		if !bytes.Equal(got, filter.Value) {
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
