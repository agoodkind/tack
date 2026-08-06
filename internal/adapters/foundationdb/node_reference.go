package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// claimReferenceKeys claims keys for a node that holds no prior claims, such
// as a fresh create. It skips the reverse-index read entirely when the type
// declares no reference keys, keeping the hot create path free of the extra
// range read.
func claimReferenceKeys(tr fdb.Transaction, orgID, nodeID uuid.UUID, keys []node.ReferenceKey) error {
	if len(keys) == 0 {
		return nil
	}
	return writeReferenceKeys(tr, orgID, nodeID, keys)
}

func writeReferenceKeys(tr fdb.Transaction, orgID, nodeID uuid.UUID, keys []node.ReferenceKey) error {
	if err := clearReferenceKeys(tr, orgID, nodeID); err != nil {
		return err
	}
	for _, key := range keys {
		forwardKey := fdb.Key(nodeReferenceKey(orgID, key.TemplateName, key.Encoded))
		existing, err := tr.Get(forwardKey).Get()
		if err != nil {
			slog.Error("node.reference.read_failed", slog.String("err", err.Error()))
			return fmt.Errorf("read reference %q for template %q: %w", key.Encoded, key.TemplateName, err)
		}
		if len(existing) == 0 {
			tr.Set(forwardKey, []byte(nodeID.String()))
			tr.Set(fdb.Key(nodeReferenceOwnedKey(orgID, nodeID, key.TemplateName)), []byte(key.Encoded))
			continue
		}
		ownerID, err := uuid.Parse(string(existing))
		if err != nil {
			slog.Error("node.reference.owner_invalid", slog.String("err", err.Error()))
			return fmt.Errorf("parse holder for reference %q and template %q: %w", key.Encoded, key.TemplateName, err)
		}
		if ownerID == nodeID {
			continue
		}
		err = fmt.Errorf("reference %q for template %q is held by node %s: %w", key.Encoded, key.TemplateName, ownerID, domain.ErrConflict)
		slog.Error("node.reference.conflict", slog.String("err", err.Error()))
		return err
	}
	return nil
}

func clearReferenceKeys(tr fdb.Transaction, orgID, nodeID uuid.UUID) error {
	keyRange, err := fdb.PrefixRange(nodeReferenceOwnedPrefix(orgID, nodeID))
	if err != nil {
		slog.Error("node.reference.prefix_failed", slog.String("err", err.Error()))
		return fmt.Errorf("prefix references for node %s: %w", nodeID, err)
	}
	ownedKeys, err := tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		slog.Error("node.reference.scan_failed", slog.String("err", err.Error()))
		return fmt.Errorf("scan references for node %s: %w", nodeID, err)
	}
	for _, ownedKey := range ownedKeys {
		fields, unpackErr := tuple.Unpack(stripPrefix(ownedKey.Key))
		if unpackErr != nil || len(fields) != 4 {
			// A malformed reverse entry cannot name its forward key. Clearing
			// the range anyway would orphan that forward claim, blocking the
			// reference forever, so fail the transaction instead.
			err := fmt.Errorf("reverse reference entry for node %s is malformed: %s", nodeID, ownedKey.Key)
			slog.Error("node.reference.reverse_entry_malformed", slog.String("err", err.Error()))
			return err
		}
		templateName, ok := fields[3].(string)
		if !ok {
			err := fmt.Errorf("reverse reference entry for node %s has a non-string template name: %s", nodeID, ownedKey.Key)
			slog.Error("node.reference.reverse_entry_malformed", slog.String("err", err.Error()))
			return err
		}
		tr.Clear(fdb.Key(nodeReferenceKey(orgID, templateName, string(ownedKey.Value))))
	}
	tr.ClearRange(keyRange)
	return nil
}

// LookupReference returns the node owning a rendered reference.
func (s *NodeStore) LookupReference(ctx context.Context, orgID uuid.UUID, templateName, encoded string) (nodeID uuid.UUID, err error) {
	defer telemetry.FDBOp(ctx, "store.node.lookup_reference")(&err)
	transaction, err := s.db.CreateTransaction()
	if err != nil {
		slog.ErrorContext(ctx, "node.reference.transaction_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("create lookup transaction for reference %q and template %q: %w", encoded, templateName, err)
	}
	defer transaction.Cancel()
	owner, err := transaction.Get(fdb.Key(nodeReferenceKey(orgID, templateName, encoded))).Get()
	if err != nil {
		slog.ErrorContext(ctx, "node.reference.lookup_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("lookup reference %q for template %q: %w", encoded, templateName, err)
	}
	if len(owner) == 0 {
		return uuid.Nil, nil
	}
	nodeID, err = uuid.Parse(string(owner))
	if err != nil {
		slog.ErrorContext(ctx, "node.reference.owner_invalid", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("parse holder for reference %q and template %q: %w", encoded, templateName, err)
	}
	return nodeID, nil
}
