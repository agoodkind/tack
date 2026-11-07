//go:build !runtimeaudit

package foundationdb

import (
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
)

func countSessionRead(_ fdb.ReadTransaction) {}

func applySession(tr fdb.Transaction, apply func(fdb.Transaction) error) error {
	return apply(tr)
}

func reportSession(_ fdb.Transaction, _ string, _ uuid.UUID, _ error) {}

func forgetSession(_ fdb.Transaction) {}
