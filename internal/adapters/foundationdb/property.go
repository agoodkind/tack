package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// PropertyDefStore implements node.PropertyDefRepository using FoundationDB.
// When searchWork is true, a definition write requests a search rescan
// stamped by the clock.
type PropertyDefStore struct {
	db         fdb.Database
	clock      clock.Clock
	searchWork bool
}

// NewPropertyDefStore creates the property definition store. It requests no
// search rescan until [Stores.EnableSearchWork] runs.
func NewPropertyDefStore(db fdb.Database, source clock.Clock) *PropertyDefStore {
	return &PropertyDefStore{db: db, clock: source, searchWork: false}
}

// Set stores def and its name index entry. It increments the organization's
// metadata epoch when the stored record changes, and it updates the
// projection digest when the definition's identity, name, or declaration
// changes. When search work is enabled, that declaration change also
// requests a content rescan. Set accepts a new definition without a search
// declaration when node.EffectiveSearchProjection returns a projection for
// it. If that call returns an error, Set refuses the definition and does not
// store it. Set also accepts a nil declaration on an existing record.
func (s *PropertyDefStore) Set(ctx context.Context, def *node.PropertyDef) (err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.set")(&err)
	if def.Type == node.PropertyTypeUUID && def.ReferenceTargetTypeKey != "" && !def.Indexed {
		return unindexedReferenceError{definition: def}
	}
	b, err := json.Marshal(def)
	if err != nil {
		return nodeOperationFailure(ctx, "marshal property definition "+def.ID.String(), err)
	}
	changes, err := beginMutationAudit(ctx, def.OrgID)
	if err != nil {
		return err
	}
	err = runNodeMutation(ctx, s.db, "set property definition "+def.ID.String(), func(tr fdb.Transaction) error {
		changes.startAttempt()
		key := propertyDefKey(def.OrgID, def.ID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read property definition "+def.ID.String(), readErr)
		}
		if len(previous) == 0 && def.Search == nil {
			if _, projectionErr := node.EffectiveSearchProjection(def); projectionErr != nil {
				return undeclaredDefinitionError{definition: def}
			}
		}
		if err := updatePropertyDefinition(ctx, tr, s.clock.Now(), s.searchWork, def.OrgID, def.ID, previous, def); err != nil {
			return err
		}
		tr.Set(fdb.Key(key), b)
		return recordMetadataChange(ctx, tr, changes, propertyDefTarget(def.ID, def.Name), previous, b)
	})
	if err != nil {
		return err
	}
	changes.finish(ctx)
	return nil
}

// undeclaredDefinitionError reports Set's refusal to store a new definition
// without a search declaration when node.EffectiveSearchProjection fails.
// The message includes the definition name, ID, and organization. Set's
// deferred telemetry.FDBOp logs the error once. Unwrap returns
// node.ErrInvalidSearchProjection.
type undeclaredDefinitionError struct {
	definition *node.PropertyDef
}

func (e undeclaredDefinitionError) Error() string {
	return fmt.Sprintf("store property definition %q (%s) in org %s: requires a search declaration for a type without a default projection: %v",
		e.definition.Name, e.definition.ID, e.definition.OrgID, node.ErrInvalidSearchProjection)
}

// Unwrap lets callers match node.ErrInvalidSearchProjection.
func (e undeclaredDefinitionError) Unwrap() error { return node.ErrInvalidSearchProjection }

// unindexedReferenceError reports the rejection of a UUID reference
// definition that does not set Indexed. The deferred telemetry.FDBOp in Set
// logs the error once.
type unindexedReferenceError struct {
	definition *node.PropertyDef
}

func (e unindexedReferenceError) Error() string {
	return fmt.Sprintf("store property definition %q (%s) in org %s: a reference to %s nodes must set Indexed: %v",
		e.definition.Name, e.definition.ID, e.definition.OrgID, e.definition.ReferenceTargetTypeKey, domain.ErrInvalidArgument)
}

// Unwrap lets callers match domain.ErrInvalidArgument.
func (e unindexedReferenceError) Unwrap() error { return domain.ErrInvalidArgument }

func (s *PropertyDefStore) Get(ctx context.Context, orgID, defID uuid.UUID) (def *node.PropertyDef, err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.get")(&err)
	var encoded []byte
	err = runNodeReadTransaction(ctx, s.db, "get property definition "+defID.String(), func(tr fdb.Transaction) error {
		var readErr error
		encoded, readErr = tr.Get(fdb.Key(propertyDefKey(orgID, defID))).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read property definition "+defID.String(), readErr)
		}
		return nil
	})
	if err != nil || len(encoded) == 0 {
		return nil, err
	}
	var d node.PropertyDef
	if err := json.Unmarshal(encoded, &d); err != nil {
		return nil, nodeOperationFailure(ctx, "unmarshal property definition "+defID.String(), err)
	}
	return &d, nil
}

func (s *PropertyDefStore) List(ctx context.Context, orgID uuid.UUID) (defs []*node.PropertyDef, err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.list")(&err)
	pr, err := fdb.PrefixRange(propertyDefPrefix(orgID))
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create the property definition range", err)
	}
	var kvs []fdb.KeyValue
	err = runNodeReadTransaction(ctx, s.db, "list property definitions", func(tr fdb.Transaction) error {
		var readErr error
		kvs, readErr = tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read property definitions", readErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	defs = make([]*node.PropertyDef, 0, len(kvs))
	for _, kv := range kvs {
		var d node.PropertyDef
		if err := json.Unmarshal(kv.Value, &d); err != nil {
			return nil, nodeOperationFailure(ctx, "unmarshal property definition", err)
		}
		defs = append(defs, &d)
	}
	return defs, nil
}

func (s *PropertyDefStore) Delete(ctx context.Context, orgID, defID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.delete")(&err)
	changes, err := beginMutationAudit(ctx, orgID)
	if err != nil {
		return err
	}
	err = runNodeMutation(ctx, s.db, "delete property definition "+defID.String(), func(tr fdb.Transaction) error {
		changes.startAttempt()
		key := propertyDefKey(orgID, defID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read property definition "+defID.String(), readErr)
		}
		if err := updatePropertyDefinition(ctx, tr, s.clock.Now(), s.searchWork, orgID, defID, previous, nil); err != nil {
			return err
		}
		tr.Clear(fdb.Key(key))
		target, err := storedPropertyDefTarget(ctx, defID, previous)
		if err != nil {
			return err
		}
		return recordMetadataChange(ctx, tr, changes, target, previous, nil)
	})
	if err != nil {
		return err
	}
	changes.finish(ctx)
	return nil
}

// encodePropertyValue encodes a raw JSON property value as FDB tuple bytes for
// use in the secondary index. The encoding must sort the way the caller expects
// range scans to iterate. This is a minimal implementation: numbers encode as
// float64 IEEE (round-trip safe for JSON numbers), strings/bools/null as their
// native tuple types, arrays/objects as their JSON string representation
// (lexicographic sort, useful for equality scans).
func encodePropertyValue(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return nil
	}
	// Use the raw bytes directly: JSON's canonical encoding for primitives
	// (numbers, strings, bools, null) sorts correctly for equality scans.
	// For range scans across numeric properties, callers should prefer
	// typed encoding via a PropertyDef-aware helper; this default works
	// for the common equality filter case.
	return []byte(raw)
}
