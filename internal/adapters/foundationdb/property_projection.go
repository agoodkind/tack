package foundationdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

const projectionPageSize = 100

// ScanProjections reads one bounded page of property definitions in key order.
func (s *PropertyDefStore) ScanProjections(ctx context.Context, cursor string) (defs []*node.PropertyDef, next string, err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.scan_projections")(&err)
	logger := telemetry.L(ctx)
	reported := false
	defer func() {
		if err != nil && !reported {
			logger.ErrorContext(ctx, "search.projection.scan_failed", slog.String("err", err.Error()))
		}
		if err != nil {
			err = node.LoggedProjectionError{Cause: err}
		}
	}()
	keyRange, err := fdb.PrefixRange(withPrefix(tuple.Tuple{keyPropertyDef}.Pack()))
	if err != nil {
		wrapped := fmt.Errorf("create property projection range: %w", err)
		logger.ErrorContext(ctx, "search.projection.scan_failed", slog.String("err", wrapped.Error()))
		reported = true
		return nil, "", wrapped
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		key, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return nil, "", fmt.Errorf("decode property projection cursor: %w", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(key))
	}
	transaction, err := s.db.CreateTransaction()
	if err != nil {
		return nil, "", fmt.Errorf("create property projection transaction: %w", err)
	}
	defer transaction.Cancel()
	var items []fdb.KeyValue
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", fmt.Errorf("scan property projections: %w", err)
		}
		items, err = transaction.GetRange(fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}, fdb.RangeOptions{Limit: projectionPageSize + 1}).GetSliceWithError()
		if err == nil {
			break
		}
		var databaseError fdb.Error
		if !errors.As(err, &databaseError) {
			return nil, "", fmt.Errorf("scan property projections: %w", err)
		}
		if err := transaction.OnError(databaseError).Get(); err != nil {
			return nil, "", fmt.Errorf("retry property projection scan: %w", err)
		}
	}
	hasMore := len(items) > projectionPageSize
	if hasMore {
		items = items[:projectionPageSize]
	}
	defs = make([]*node.PropertyDef, 0, len(items))
	for _, item := range items {
		var definition node.PropertyDef
		if err := json.Unmarshal(item.Value, &definition); err != nil {
			return nil, "", fmt.Errorf("decode scanned property definition: %w", err)
		}
		defs = append(defs, &definition)
	}
	if hasMore {
		next = base64.RawURLEncoding.EncodeToString(items[len(items)-1].Key)
	}
	return defs, next, nil
}

// SetProjectionIfNil atomically applies one declaration or accepts an equal one.
func (s *PropertyDefStore) SetProjectionIfNil(ctx context.Context, definition *node.PropertyDef, projection node.SearchProjection) (changed bool, err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.set_projection")(&err)
	logger := telemetry.L(ctx)
	reported := false
	defer func() {
		if err != nil && !reported {
			logger.ErrorContext(ctx, "search.projection.set_failed", slog.String("err", err.Error()), slog.String("property_definition_id", definition.ID.String()))
		}
		if err != nil {
			err = node.LoggedProjectionError{Cause: err}
		}
	}()
	transaction, err := s.db.CreateTransaction()
	if err != nil {
		wrapped := fmt.Errorf("create property projection transaction: %w", err)
		logger.ErrorContext(ctx, "search.projection.set_failed", slog.String("err", wrapped.Error()), slog.String("property_definition_id", definition.ID.String()))
		reported = true
		return false, wrapped
	}
	defer transaction.Cancel()
	for {
		reported = false
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("set property projection %s: %w", definition.ID, err)
		}
		changed, err = setProjectionInTransaction(ctx, transaction, definition, projection)
		if err != nil {
			reported = true
		}
		if err == nil && changed {
			if commitErr := transaction.Commit().Get(); commitErr != nil {
				err = fmt.Errorf("commit projection for property definition %s: %w", definition.ID, commitErr)
			}
		}
		if err == nil {
			if changed {
				commitStagedIntent(ctx)
			}
			return changed, nil
		}
		var databaseError fdb.Error
		if !errors.As(err, &databaseError) {
			return false, err
		}
		if retryErr := transaction.OnError(databaseError).Get(); retryErr != nil {
			return false, fmt.Errorf("retry property projection %s: %w", definition.ID, retryErr)
		}
	}
}

// setProjectionInTransaction stores projection on one definition without a
// declaration. It writes the definition record, its property-name index
// entry, and the audit intent. It changes no projection digest and schedules
// no search work because the initial rollout runs before the first
// FoundationDB rebuild, and that rebuild reads the completed declarations.
func setProjectionInTransaction(ctx context.Context, tr fdb.Transaction, definition *node.PropertyDef, projection node.SearchProjection) (changed bool, err error) {
	logger := telemetry.L(ctx)
	reported := false
	defer func() {
		if err != nil && !reported {
			logger.ErrorContext(ctx, "search.projection.transaction_failed", slog.String("err", err.Error()), slog.String("property_definition_id", definition.ID.String()))
		}
		if err != nil {
			err = node.LoggedProjectionError{Cause: err}
		}
	}()
	key := fdb.Key(propertyDefKey(definition.OrgID, definition.ID))
	value, readErr := tr.Get(key).Get()
	if readErr != nil {
		wrapped := fmt.Errorf("read property definition %s: %w", definition.ID, readErr)
		logger.ErrorContext(ctx, "search.projection.transaction_failed", slog.String("err", wrapped.Error()), slog.String("property_definition_id", definition.ID.String()))
		reported = true
		return false, wrapped
	}
	if len(value) == 0 {
		return false, fmt.Errorf("property definition %s no longer exists", definition.ID)
	}
	var current node.PropertyDef
	if decodeErr := json.Unmarshal(value, &current); decodeErr != nil {
		return false, fmt.Errorf("decode property definition %s: %w", definition.ID, decodeErr)
	}
	if current.OrgID != definition.OrgID || current.ID != definition.ID {
		return false, fmt.Errorf("property definition %s identity changed", definition.ID)
	}
	if current.Search != nil {
		left, marshalErr := json.Marshal(current.Search)
		if marshalErr != nil {
			return false, fmt.Errorf("encode stored projection %s: %w", definition.ID, marshalErr)
		}
		right, marshalErr := json.Marshal(projection)
		if marshalErr != nil {
			return false, fmt.Errorf("encode manifest projection %s: %w", definition.ID, marshalErr)
		}
		if string(left) != string(right) {
			return false, fmt.Errorf("property definition %s has a conflicting projection", definition.ID)
		}
		return false, nil
	}
	current.Search = &projection
	encoded, marshalErr := json.Marshal(current)
	if marshalErr != nil {
		return false, fmt.Errorf("encode property definition %s: %w", definition.ID, marshalErr)
	}
	indexPropertyName(tr, definition.OrgID, definition.ID, nil, &current)
	tr.Set(key, encoded)
	if intentErr := writeStagedIntent(ctx, tr); intentErr != nil {
		return false, fmt.Errorf("record projection audit for property definition %s: %w", definition.ID, intentErr)
	}
	return true, nil
}
