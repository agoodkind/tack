package foundationdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

const maxStrictScanNodes = 100

// StrictActivation reads the strict validation activation record.
func (s *NodeStore) StrictActivation(ctx context.Context) (activation node.StrictActivation, err error) {
	defer telemetry.FDBOp(ctx, "store.strict.activation")(&err)
	err = runNodeReadTransaction(ctx, s.db, "read strict validation activation", func(tr fdb.Transaction) error {
		var readErr error
		activation, readErr = readStrictActivation(ctx, tr)
		return readErr
	})
	return activation, err
}

// ActivateStrict compares the current bucket epochs and metadata epochs with
// evidence in one transaction. It writes the activation record only when all
// epochs match. If any epoch differs, it does not write the record and returns
// the changes with [node.ErrStrictEvidenceChanged]. If strict validation is
// already active, it returns [domain.ErrConflict].
func (s *NodeStore) ActivateStrict(ctx context.Context, evidence node.StrictEvidence, activation node.StrictActivation) (change node.StrictEvidenceChange, err error) {
	defer telemetry.FDBOp(ctx, "store.strict.activate")(&err)
	encoded, err := json.Marshal(activation)
	if err != nil {
		slog.ErrorContext(ctx, "strict.activation_failed", slog.String("err", err.Error()))
		return change, fmt.Errorf("encode the strict validation activation record: %w", err)
	}
	err = runNodeMutation(ctx, s.db, "activate strict validation", func(tr fdb.Transaction) error {
		current, readErr := readStrictActivation(ctx, tr)
		if readErr != nil {
			return readErr
		}
		if current.Active {
			return fmt.Errorf("strict validation is already active since scan %s: %w", current.ScanID, domain.ErrConflict)
		}
		later, readErr := readStrictEvidence(ctx, tr)
		if readErr != nil {
			return readErr
		}
		change = evidence.Compare(later)
		if !change.Empty() {
			return fmt.Errorf("activate strict validation: %w", node.ErrStrictEvidenceChanged)
		}
		tr.Set(fdb.Key(strictActivationKey()), encoded)
		return nil
	})
	if err != nil {
		return change, err
	}
	telemetry.L(ctx).InfoContext(ctx, "strict.activated", slog.String("scan_id", activation.ScanID.String()), slog.Int("records_validated", activation.RecordsValidated))
	return change, nil
}

// StrictScanPage reads at most limit primary node records across all
// organizations in key order after cursor. In the same transaction, it reads
// the graph of each record with readStrictGraph. The next cursor is empty
// after the last page.
func (s *NodeStore) StrictScanPage(ctx context.Context, cursor string, limit int) (records []node.StrictRecord, next string, err error) {
	defer telemetry.FDBOp(ctx, "store.strict.scan_page")(&err)
	if limit <= 0 || limit > maxStrictScanNodes {
		slog.ErrorContext(ctx, "strict.scan_page_failed", slog.Int("limit", limit), slog.String("err", domain.ErrInvalidArgument.Error()))
		return nil, "", fmt.Errorf("strict scan limit must be between 1 and %d: %w", maxStrictScanNodes, domain.ErrInvalidArgument)
	}
	var items []fdb.KeyValue
	err = runNodeReadTransaction(ctx, s.db, "read strict scan page", func(tr fdb.Transaction) error {
		keyRange, rangeErr := fdb.PrefixRange(nodeInstancePrefix())
		if rangeErr != nil {
			return searchReadFailure(ctx, "create the strict scan range", rangeErr)
		}
		begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
		if cursor != "" {
			lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
			if decodeErr != nil {
				return searchReadFailure(ctx, "decode the strict scan cursor", decodeErr)
			}
			begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
		}
		selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
		var readErr error
		next = ""
		items, readErr = tr.GetRange(selection, fdb.RangeOptions{Limit: limit + 1}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read the strict scan page", readErr)
		}
		if len(items) > limit {
			items = items[:limit]
			next = base64.RawURLEncoding.EncodeToString(items[limit-1].Key)
		}
		records, readErr = readStrictRecords(ctx, tr, items)
		return readErr
	})
	if err != nil {
		return nil, "", err
	}
	return records, next, nil
}

func readStrictRecords(ctx context.Context, tr fdb.Transaction, items []fdb.KeyValue) ([]node.StrictRecord, error) {
	definitions := map[uuid.UUID][]*node.PropertyDef{}
	records := make([]node.StrictRecord, 0, len(items))
	for _, item := range items {
		var record node.Node
		if err := json.Unmarshal(item.Value, &record); err != nil {
			return nil, searchReadFailure(ctx, "decode a scanned node record", err)
		}
		orgDefinitions, cached := definitions[record.OrgID]
		if !cached {
			var err error
			orgDefinitions, err = readSchemaRecords[node.PropertyDef](ctx, tr, propertyDefPrefix(record.OrgID), "property definitions")
			if err != nil {
				return nil, err
			}
			definitions[record.OrgID] = orgDefinitions
		}
		graph, err := readStrictGraph(ctx, tr, orgDefinitions, &record)
		if err != nil {
			return nil, err
		}
		records = append(records, node.StrictRecord{Node: &record, Graph: graph})
	}
	return records, nil
}
