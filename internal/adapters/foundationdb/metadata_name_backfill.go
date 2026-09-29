package foundationdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// nameIndexBackfillPageSize bounds the metadata records that one backfill
// transaction reads.
const nameIndexBackfillPageSize = 100

// NameIndexPage reports one backfill page of a metadata name index. Scanned
// counts the records the page read. Missing counts the records without an
// index entry, which the page writes when the caller asks it to. Next is the
// cursor of the following page, or empty after the last page.
type NameIndexPage struct {
	Scanned int
	Missing int
	Next    string
}

// BackfillNameIndex reads one page of node types of every organization after
// cursor and finds the node types without a type-key index entry. When write
// is true, the same transaction writes each missing entry.
func (s *NodeTypeStore) BackfillNameIndex(ctx context.Context, cursor string, write bool) (page NameIndexPage, err error) {
	defer telemetry.FDBOp(ctx, "store.node_type.backfill_name_index")(&err)
	return backfillNameIndexPage(ctx, s.db, keyNodeTypeDef, cursor, write, func(orgID, typeID uuid.UUID, encoded []byte) ([]byte, error) {
		var kind node.NodeType
		if err := json.Unmarshal(encoded, &kind); err != nil {
			return nil, searchReadFailure(ctx, "decode node type "+typeID.String(), err)
		}
		if kind.TypeKey == "" {
			return nil, nil
		}
		return nodeTypeByKeyKey(orgID, kind.TypeKey, typeID), nil
	})
}

// BackfillNameIndex reads one page of property definitions of every
// organization after cursor and finds the definitions without a
// property-name index entry. When write is true, the same transaction
// writes each missing entry.
func (s *PropertyDefStore) BackfillNameIndex(ctx context.Context, cursor string, write bool) (page NameIndexPage, err error) {
	defer telemetry.FDBOp(ctx, "store.property_def.backfill_name_index")(&err)
	return backfillNameIndexPage(ctx, s.db, keyPropertyDef, cursor, write, func(orgID, definitionID uuid.UUID, encoded []byte) ([]byte, error) {
		var definition node.PropertyDef
		if err := json.Unmarshal(encoded, &definition); err != nil {
			return nil, searchReadFailure(ctx, "decode property definition "+definitionID.String(), err)
		}
		return propertyDefByNameKey(orgID, definition.Name, definitionID), nil
	})
}

// nameIndexEntry returns the index entry key of one stored record, or nil
// when the record has no entry.
type nameIndexEntry func(orgID, recordID uuid.UUID, encoded []byte) ([]byte, error)

func backfillNameIndexPage(ctx context.Context, database fdb.Database, family, cursor string, write bool, entryFor nameIndexEntry) (NameIndexPage, error) {
	operation := "backfill name index of " + family
	var page NameIndexPage
	apply := func(tr fdb.Transaction) error {
		items, next, err := readNameIndexRecords(ctx, tr, family, cursor)
		if err != nil {
			return err
		}
		page = NameIndexPage{Scanned: len(items), Missing: 0, Next: next}
		entries := make([][]byte, 0, len(items))
		for _, item := range items {
			entry, err := recordIndexEntry(ctx, item, entryFor)
			if err != nil {
				return err
			}
			if entry != nil {
				entries = append(entries, entry)
			}
		}
		futures := make([]fdb.FutureByteSlice, 0, len(entries))
		for _, entry := range entries {
			futures = append(futures, tr.Get(fdb.Key(entry)))
		}
		for index, future := range futures {
			stored, err := future.Get()
			if err != nil {
				return searchReadFailure(ctx, "read name index entry", err)
			}
			if stored != nil {
				continue
			}
			page.Missing++
			if write {
				tr.Set(fdb.Key(entries[index]), []byte{})
			}
		}
		return nil
	}
	var err error
	if write {
		err = runNodeMutation(ctx, database, operation, apply)
	} else {
		err = runNodeReadTransaction(ctx, database, operation, apply)
	}
	if err != nil {
		return NameIndexPage{Scanned: 0, Missing: 0, Next: ""}, err
	}
	return page, nil
}

// readNameIndexRecords reads up to nameIndexBackfillPageSize records of
// family after cursor, across every organization, in key order.
func readNameIndexRecords(ctx context.Context, tr fdb.Transaction, family, cursor string) ([]fdb.KeyValue, string, error) {
	keyRange, err := fdb.PrefixRange(withPrefix(tuple.Tuple{family}.Pack()))
	if err != nil {
		return nil, "", searchReadFailure(ctx, "create "+family+" range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", searchReadFailure(ctx, "decode "+family+" cursor", err)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	items, err := tr.GetRange(selection, fdb.RangeOptions{Limit: nameIndexBackfillPageSize + 1}).GetSliceWithError()
	if err != nil {
		return nil, "", searchReadFailure(ctx, "read "+family+" page", err)
	}
	if len(items) <= nameIndexBackfillPageSize {
		return items, "", nil
	}
	items = items[:nameIndexBackfillPageSize]
	return items, base64.RawURLEncoding.EncodeToString(items[len(items)-1].Key), nil
}

// recordIndexEntry decodes the organization and record IDs from one record
// key, (family, orgID, recordID), and returns the record's index entry key.
func recordIndexEntry(ctx context.Context, item fdb.KeyValue, entryFor nameIndexEntry) ([]byte, error) {
	values, err := tuple.Unpack(stripPrefix(item.Key))
	if err != nil {
		return nil, searchReadFailure(ctx, "unpack metadata key", err)
	}
	if len(values) != 3 {
		return nil, fmt.Errorf("metadata key has %d tuple elements, want 3", len(values))
	}
	orgText, orgIsText := values[1].(string)
	recordText, recordIsText := values[2].(string)
	if !orgIsText || !recordIsText {
		return nil, errors.New("metadata key identifiers are not strings")
	}
	orgID, err := uuid.Parse(orgText)
	if err != nil {
		return nil, searchReadFailure(ctx, "parse metadata organization "+orgText, err)
	}
	recordID, err := uuid.Parse(recordText)
	if err != nil {
		return nil, searchReadFailure(ctx, "parse metadata record "+recordText, err)
	}
	return entryFor(orgID, recordID, item.Value)
}
