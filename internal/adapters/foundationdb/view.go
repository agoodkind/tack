package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// ViewStore implements node.NodeReader using FDB NodeListView materialized records.
// All reads go through node_list_view keys; the legacy node_instance hydration path
// is bypassed entirely once views are written on every entity write.
type ViewStore struct {
	db fdb.Database
}

func NewViewStore(db fdb.Database) *ViewStore {
	return &ViewStore{db: db}
}

// Get resolves a node by ID using the global resolution record, then fetches its
// NodeListView. Two FDB read transactions: one for node_resolve, one for node_list_view.
func (s *ViewStore) Get(ctx context.Context, nodeID uuid.UUID) (*node.NodeListView, error) {
	// Step 1: resolve org context from global resolution record.
	resolveVal, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	})
	if err != nil {
		telemetry.L(ctx).Error("fdb.view.Get: resolve read failed", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("view get resolve: %w", err)
	}
	rb, ok := resolveVal.([]byte)
	if !ok || len(rb) == 0 {
		return nil, nil
	}
	var resolve node.NodeResolve
	if err := json.Unmarshal(rb, &resolve); err != nil {
		return nil, fmt.Errorf("unmarshal node resolve: %w", err)
	}

	// Step 2: fetch the materialized view.
	viewVal, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(nodeListViewKey(resolve.OrgID, resolve.WorkspaceID, resolve.NodeType, nodeID))).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("view get list view: %w", err)
	}
	vb, ok := viewVal.([]byte)
	if !ok || len(vb) == 0 {
		return nil, nil
	}
	var view node.NodeListView
	if err := json.Unmarshal(vb, &view); err != nil {
		return nil, fmt.Errorf("unmarshal node list view: %w", err)
	}
	return &view, nil
}

// Resolve returns the org/workspace/type context for any entity UUID.
// Reads the global (node_resolve, entityID) record: a single FDB point read.
func (s *ViewStore) Resolve(ctx context.Context, entityID uuid.UUID) (*node.NodeResolve, error) {
	key := fdb.Key(nodeResolveKey(entityID))
	var result *node.NodeResolve
	_, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		b := tr.Get(key).MustGet()
		if len(b) == 0 {
			return nil, domain.ErrNotFound
		}
		var r node.NodeResolve
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("unmarshal node resolve: %w", err)
		}
		result = &r
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// List fetches all NodeListViews matching q. It uses a planScan to get nodeID
// boundaries from the secondary index, then fires all chunk fetches in parallel
// via errgroup. In-memory post-filters are applied before returning.
func (s *ViewStore) List(ctx context.Context, q node.NodeListQuery) ([]*node.NodeListView, error) {
	chunks, err := s.planScan(q)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	results := make([][]*node.NodeListView, len(chunks))
	g, _ := errgroup.WithContext(ctx)
	for i, r := range chunks {
		i, r := i, r
		g.Go(func() error {
			views, err := s.fetchChunk(q.OrgID, q.WorkspaceID, q.NodeType, r)
			if err != nil {
				return err
			}
			results[i] = views
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		telemetry.L(ctx).Error("fdb.view.List: chunk fetch failed",
			slog.String("node_type", q.NodeType),
			slog.String("workspace_id", q.WorkspaceID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("view list: %w", err)
	}

	var out []*node.NodeListView
	for _, chunk := range results {
		for _, v := range chunk {
			if applyPostFilters(v, q) {
				out = append(out, v)
			}
		}
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Stream fires parallel chunk fetches and sends results to a buffered channel as
// each chunk completes. The channel is closed when all chunks finish or on error.
func (s *ViewStore) Stream(ctx context.Context, q node.NodeListQuery) (<-chan node.NodeStreamResult, error) {
	chunks, err := s.planScan(q)
	if err != nil {
		return nil, err
	}
	ch := make(chan node.NodeStreamResult, 64)
	if len(chunks) == 0 {
		close(ch)
		return ch, nil
	}

	go func() {
		defer close(ch)
		g, gctx := errgroup.WithContext(ctx)
		for _, r := range chunks {
			r := r
			g.Go(func() error {
				views, err := s.fetchChunk(q.OrgID, q.WorkspaceID, q.NodeType, r)
				if err != nil {
					select {
					case ch <- node.NodeStreamResult{Err: err}:
					case <-gctx.Done():
					}
					return err
				}
				for _, v := range views {
					if applyPostFilters(v, q) {
						select {
						case ch <- node.NodeStreamResult{View: v}:
						case <-gctx.Done():
							return gctx.Err()
						}
					}
				}
				return nil
			})
		}
		g.Wait() //nolint:errcheck // errors already sent on channel
	}()

	return ch, nil
}

// chunkRange is a pair of nodeIDs bounding one fetch chunk.
type chunkRange struct {
	nodeIDs []uuid.UUID
}

// planScan reads the appropriate secondary index (key-only, no values) and
// partitions the result into chunks of chunkSize for parallel hydration.
func (s *ViewStore) planScan(q node.NodeListQuery) ([]chunkRange, error) {
	const chunkSize = 200

	var nodeIDs []uuid.UUID

	switch {
	case q.ByProject != nil:
		prefix := tuple.Tuple{keyNodeInstanceByProject, q.OrgID.String(), q.ByProject.String(), q.NodeType}.Pack()
		pr, err := fdb.PrefixRange(prefix)
		if err != nil {
			return nil, err
		}
		kvs, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
			return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		})
		if err != nil {
			return nil, fmt.Errorf("plan scan by project: %w", err)
		}
		for _, kv := range kvs.([]fdb.KeyValue) {
			t, err := tuple.Unpack(kv.Key)
			if err != nil || len(t) < 5 {
				continue
			}
			s, ok := t[4].(string)
			if !ok {
				continue
			}
			id, err := uuid.Parse(s)
			if err != nil {
				continue
			}
			nodeIDs = append(nodeIDs, id)
		}

	case q.ByState != nil:
		nodeIDs = extractLastUUIDs(s.listByStateKVs(q.OrgID, q.WorkspaceID, q.NodeType, *q.ByState), 5)

	case q.ByProperty != nil:
		encoded := encodeForIndex(q.ByProperty.Value)
		prefix := tuple.Tuple{keyNodeByProperty, q.OrgID.String(), q.WorkspaceID.String(), q.NodeType, q.ByProperty.PropDefID.String(), encoded}.Pack()
		pr, err := fdb.PrefixRange(prefix)
		if err != nil {
			return nil, err
		}
		idxVals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
			return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		})
		if err != nil {
			return nil, fmt.Errorf("plan scan by property: %w", err)
		}
		// key: (keyNodeByProperty, orgID, workspaceID, nodeType, propDefID, encodedValue, nodeID)
		nodeIDs = extractLastUUIDs(idxVals.([]fdb.KeyValue), 6)

	default:
		// Full workspace scan: range scan on (node_list_view, orgID, wsID, nodeType, *).
		prefix := tuple.Tuple{keyNodeListView, q.OrgID.String(), q.WorkspaceID.String(), q.NodeType}.Pack()
		pr, err := fdb.PrefixRange(prefix)
		if err != nil {
			return nil, err
		}
		kvs, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
			return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		})
		if err != nil {
			return nil, fmt.Errorf("plan scan workspace: %w", err)
		}
		// key: (node_list_view, orgID, wsID, nodeType, nodeID)
		nodeIDs = extractLastUUIDs(kvs.([]fdb.KeyValue), 4)
	}

	if len(nodeIDs) == 0 {
		return nil, nil
	}

	var chunks []chunkRange
	for i := 0; i < len(nodeIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(nodeIDs) {
			end = len(nodeIDs)
		}
		chunks = append(chunks, chunkRange{nodeIDs: nodeIDs[i:end]})
	}
	return chunks, nil
}

// listByStateKVs reads the state secondary index and returns raw key-values.
func (s *ViewStore) listByStateKVs(orgID, workspaceID uuid.UUID, nodeType string, stateID uuid.UUID) []fdb.KeyValue {
	prefix := tuple.Tuple{keyNodeInstanceByState, orgID.String(), workspaceID.String(), nodeType, stateID.String()}.Pack()
	pr, _ := fdb.PrefixRange(prefix)
	kvs, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil
	}
	return kvs.([]fdb.KeyValue)
}

// fetchChunk fetches NodeListView records for all nodeIDs in one FDB read
// transaction using parallel futures. Safe to call concurrently with other chunks.
func (s *ViewStore) fetchChunk(orgID, workspaceID uuid.UUID, nodeType string, r chunkRange) ([]*node.NodeListView, error) {
	if len(r.nodeIDs) == 0 {
		return nil, nil
	}
	raw, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		futures := make([]fdb.FutureByteSlice, len(r.nodeIDs))
		for i, id := range r.nodeIDs {
			futures[i] = tr.Get(fdb.Key(nodeListViewKey(orgID, workspaceID, nodeType, id)))
		}
		results := make([][]byte, len(futures))
		for i, f := range futures {
			b, err := f.Get()
			if err != nil {
				return nil, err
			}
			results[i] = b
		}
		return results, nil
	})
	if err != nil {
		return nil, fmt.Errorf("fetch chunk: %w", err)
	}
	byteSlices := raw.([][]byte)
	views := make([]*node.NodeListView, 0, len(byteSlices))
	for _, b := range byteSlices {
		if len(b) == 0 {
			continue
		}
		var v node.NodeListView
		if err := json.Unmarshal(b, &v); err != nil {
			continue
		}
		views = append(views, &v)
	}
	return views, nil
}

// applyPostFilters returns true when v satisfies all post-scan filters in q.
func applyPostFilters(v *node.NodeListView, q node.NodeListQuery) bool {
	if q.FilterIsDraft != nil && v.IsDraft != *q.FilterIsDraft {
		return false
	}
	if q.FilterAssignee != nil {
		found := false
		for _, id := range v.AssigneeIDs {
			if id == *q.FilterAssignee {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
