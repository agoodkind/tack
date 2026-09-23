package foundationdb

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// IndexMetadata writes the lookup index entries of one bounded page of the
// organization's property definitions or node types and checkpoints the
// metadata pass. Each definition and type write updates these entries. This
// pass writes the entries of records written before the indexes existed.
func (s *SearchWorkStore) IndexMetadata(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.index_metadata")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if _, err := s.verifyClaim(ctx, tr, work); err != nil {
			return err
		}
		state, found, err := readScanState(ctx, tr, work.OrgID)
		if err != nil || !found {
			return claimMismatch(err)
		}
		var prefix []byte
		switch state.Phase {
		case scanDefinitions:
			prefix = propertyDefPrefix(work.OrgID)
		case scanTypes:
			prefix = nodeTypeDefPrefix(work.OrgID)
		default:
			return searchdomain.ErrWorkChanged
		}
		items, done, err := readMetadataPage(ctx, tr, prefix, state.Cursor)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := indexMetadataRecord(ctx, tr, work.OrgID, state.Phase, item.Value); err != nil {
				return err
			}
		}
		state.Cursor = ""
		switch {
		case !done:
			state.Cursor = base64.RawURLEncoding.EncodeToString(items[len(items)-1].Key)
		case state.Phase == scanDefinitions:
			state.Phase = scanTypes
		default:
			state.Phase = scanNodes
		}
		return writeSearchRecord(ctx, tr, searchCursorKey(string(work.Class), work.OrgID, uuid.Nil), state)
	})
	if err != nil {
		return searchStorageError(ctx, "search.rescan.metadata_failed", "index metadata of organization "+work.OrgID.String(), work.NodeID, err)
	}
	return nil
}

func readMetadataPage(ctx context.Context, tr fdb.Transaction, prefix []byte, cursor string) ([]fdb.KeyValue, bool, error) {
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, false, searchReadFailure(ctx, "create metadata range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return nil, false, searchReadFailure(ctx, "decode metadata cursor", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	items, err := tr.GetRange(selection, fdb.RangeOptions{Limit: maxSearchScanNodes + 1}).GetSliceWithError()
	if err != nil {
		return nil, false, searchReadFailure(ctx, "read metadata page", err)
	}
	if len(items) > maxSearchScanNodes {
		return items[:maxSearchScanNodes], false, nil
	}
	return items, true, nil
}

func indexMetadataRecord(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, phase string, encoded []byte) error {
	if phase == scanDefinitions {
		var definition node.PropertyDef
		if err := json.Unmarshal(encoded, &definition); err != nil {
			return searchReadFailure(ctx, "decode property definition", err)
		}
		tr.Set(fdb.Key(propertyDefByNameKey(orgID, definition.Name, definition.ID)), []byte{})
		return nil
	}
	var kind node.NodeType
	if err := json.Unmarshal(encoded, &kind); err != nil {
		return searchReadFailure(ctx, "decode node type", err)
	}
	if kind.TypeKey != "" {
		tr.Set(fdb.Key(nodeTypeByKeyKey(orgID, kind.TypeKey, kind.ID)), []byte{})
	}
	return nil
}
