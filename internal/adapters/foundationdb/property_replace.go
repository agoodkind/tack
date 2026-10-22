package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// Replace decodes and re-encodes the stored definition before comparing
// it with before. It stores after and returns true only when the re-encoded
// stored definition matches before.
// The same transaction updates the name index, metadata epoch, and
// projection digests and writes one change event with the stored and
// replacement records. When search
// work is enabled, a rendering input change also requests a content rescan
// in that transaction. If the stored definition does not match before,
// Replace does not write and returns false.
func (s *PropertyDefStore) Replace(ctx context.Context, before, after *node.PropertyDef) (replaced bool, err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.replace")(&err)
	expected, err := json.Marshal(before)
	if err != nil {
		slog.ErrorContext(ctx, "store.property_def.replace_failed", slog.String("err", err.Error()), slog.String("property_definition_id", before.ID.String()))
		return false, fmt.Errorf("marshal expected property def %s: %w", before.ID, err)
	}
	encoded, err := json.Marshal(after)
	if err != nil {
		slog.ErrorContext(ctx, "store.property_def.replace_failed", slog.String("err", err.Error()), slog.String("property_definition_id", after.ID.String()))
		return false, fmt.Errorf("marshal replacement property def %s: %w", after.ID, err)
	}
	changes, err := beginMutationAudit(ctx, after.OrgID)
	if err != nil {
		return false, err
	}
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		changes.startAttempt()
		key := propertyDefKey(after.OrgID, after.ID)
		previous, readErr := tr.Get(fdb.Key(key)).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read property definition "+after.ID.String(), readErr)
		}
		current, matchErr := storedDefinitionEquals(ctx, previous, expected)
		replaced = current
		if matchErr != nil || !current {
			return matchErr
		}
		if err := updatePropertyDefinition(ctx, tr, s.clock.Now(), s.searchWork, after.OrgID, after.ID, previous, after); err != nil {
			return err
		}
		tr.Set(fdb.Key(key), encoded)
		return recordMetadataChange(ctx, tr, changes, propertyDefTarget(after.ID, after.Name), previous, encoded)
	})
	if err != nil {
		return false, err
	}
	changes.finish(ctx)
	return replaced, nil
}

// storedDefinitionEquals decodes stored as a property definition and
// re-encodes it as JSON for comparison with expected. It returns true when
// the encoded bytes match expected. An absent record does not match.
func storedDefinitionEquals(ctx context.Context, stored, expected []byte) (bool, error) {
	if len(stored) == 0 {
		return false, nil
	}
	var decoded node.PropertyDef
	if err := json.Unmarshal(stored, &decoded); err != nil {
		return false, searchReadFailure(ctx, "decode stored property definition", err)
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return false, searchReadFailure(ctx, "encode stored property definition", err)
	}
	return bytes.Equal(normalized, expected), nil
}
