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

// maxWarningPage limits the number of warnings in one listing read.
const maxWarningPage = 100

// SearchWarnings reads at most maxWarningPage verified property warnings
// across all organizations after cursor. An empty cursor starts at the first
// warning.
func (s *Stores) SearchWarnings(ctx context.Context, cursor string) (page searchdomain.WarningPage, err error) {
	defer telemetry.FDBOp(ctx, "store.search_warning.list")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		items, readErr := readWarningItems(ctx, tr, cursor)
		if readErr != nil {
			return readErr
		}
		examined := items[:min(len(items), maxWarningPage)]
		page = searchdomain.WarningPage{Warnings: make([]node.PropertyDiagnostic, 0, len(examined)), NextCursor: "", Done: len(items) <= maxWarningPage}
		for _, item := range examined {
			var warning node.PropertyDiagnostic
			if decodeErr := json.Unmarshal(item.Value, &warning); decodeErr != nil {
				return searchReadFailure(ctx, "decode search warning", decodeErr)
			}
			page.Warnings = append(page.Warnings, warning)
		}
		if !page.Done {
			page.NextCursor = base64.RawURLEncoding.EncodeToString(examined[len(examined)-1].Key)
		}
		return nil
	})
	if err != nil {
		return page, searchStorageError(ctx, "search.warning.list_failed", "list search warnings", uuid.Nil, err)
	}
	return page, nil
}

// readWarningItems reads up to one page plus one verified warning after
// cursor. An extra warning means another page exists.
func readWarningItems(ctx context.Context, tr fdb.Transaction, cursor string) ([]fdb.KeyValue, error) {
	keyRange, err := fdb.PrefixRange(searchWarningFamilyPrefix())
	if err != nil {
		return nil, searchReadFailure(ctx, "create search warning range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return nil, searchReadFailure(ctx, "decode search warning cursor", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	items, err := tr.GetRange(selection, fdb.RangeOptions{Limit: maxWarningPage + 1}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read search warning page", err)
	}
	return items, nil
}
