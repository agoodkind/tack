package foundationdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

const (
	parentPropertyIndexTupleSize = 6
	parentPropertyNamePosition   = 3
)

var (
	errPropertyIndexTupleSize = errors.New("property index key has an invalid tuple length")
	errPropertyIndexNameType  = errors.New("property index property name is not a string")
)

// ParentPropertyIndexPage reports one bounded page of legacy property indexes.
// Removed counts parent_id keys that the page removes when write is true.
// Next selects the following page, including pages without matching keys.
type ParentPropertyIndexPage struct {
	Removed int
	Next    string
}

// RetireParentPropertyIndexes scans property indexes across every organization.
//
// It counts parent_id keys and clears them when write is true, including stale
// values that the primary node and read view no longer store.
//
// For example, a dry run starts with an empty cursor:
//
//	page, err := store.RetireParentPropertyIndexes(ctx, "", false)
func (s *NodeStore) RetireParentPropertyIndexes(
	ctx context.Context,
	cursor string,
	write bool,
) (ParentPropertyIndexPage, error) {
	var operationErr error
	defer telemetry.FDBOp(ctx, "store.node.retire_parent_property_indexes")(&operationErr)
	page, operationErr := s.retireParentPropertyIndexes(ctx, cursor, write)

	return page, operationErr
}

func (s *NodeStore) retireParentPropertyIndexes(
	ctx context.Context,
	cursor string,
	write bool,
) (ParentPropertyIndexPage, error) {
	const operation = "retire parent property indexes"
	if write {
		if _, err := audit.RequireMutationContext(ctx); err != nil {
			return ParentPropertyIndexPage{}, nodeOperationFailure(ctx, operation, err)
		}
	}

	page := ParentPropertyIndexPage{Removed: 0, Next: ""}
	apply := func(transaction fdb.Transaction) error {
		items, next, err := readNameIndexRecords(ctx, transaction, keyNodeByProperty, cursor)
		if err != nil {
			return err
		}

		page = ParentPropertyIndexPage{Removed: 0, Next: next}

		for _, item := range items {
			matches, err := legacyParentIndexKey(ctx, item.Key)
			if err != nil {
				return err
			}

			if !matches {
				continue
			}

			page.Removed++
			if write {
				transaction.Clear(item.Key)
			}
		}

		return nil
	}
	var err error
	if write {
		err = runNodeMutation(ctx, s.db, operation, apply)
	} else {
		err = runNodeReadTransaction(ctx, s.db, operation, apply)
	}

	if err != nil {
		return ParentPropertyIndexPage{}, err
	}

	return page, nil
}

func legacyParentIndexKey(ctx context.Context, key fdb.Key) (bool, error) {
	const operation = "decode property index key for parent retirement"
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return false, nodeOperationFailure(ctx, operation, err)
	}

	if len(values) != parentPropertyIndexTupleSize {
		return false, nodeOperationFailure(
			ctx,
			operation,
			fmt.Errorf(
				"got %d tuple elements, want %d: %w",
				len(values),
				parentPropertyIndexTupleSize,
				errPropertyIndexTupleSize,
			),
		)
	}

	propertyName, isText := values[parentPropertyNamePosition].(string)
	if !isText {
		return false, nodeOperationFailure(
			ctx,
			operation,
			fmt.Errorf(
				"element %d has type %T: %w",
				parentPropertyNamePosition,
				values[parentPropertyNamePosition],
				errPropertyIndexNameType,
			),
		)
	}

	return propertyName == legacyParentProperty, nil
}
