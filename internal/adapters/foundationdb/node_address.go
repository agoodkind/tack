package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// GetAddress returns the node ID registered under a node type address value.
func (s *NodeStore) GetAddress(ctx context.Context, nodeType string, addressKind node.AddressKind, address string) (id uuid.UUID, err error) {
	defer telemetry.FDBOp(ctx, "store.node.get_address")(&err)
	log := telemetry.L(ctx)
	addressKey := fdb.Key(addressIndexKey(nodeType, addressKind, address))
	for {
		tr, err := s.db.CreateTransaction()
		if err != nil {
			log.ErrorContext(ctx, "node.address.transaction.create", slog.String("err", err.Error()))
			return uuid.Nil, fmt.Errorf("create address lookup transaction: %w", err)
		}
		value, err := tr.Get(addressKey).Get()
		if err != nil {
			retry, retryErr := retryAddressTransaction(ctx, tr, "node.address.get.retry", err)
			tr.Cancel()
			if retryErr != nil {
				return uuid.Nil, retryErr
			}
			if retry {
				continue
			}
			log.ErrorContext(ctx, "node.address.get",
				slog.String("node_type", nodeType),
				slog.String("address_kind", string(addressKind)),
				slog.String("address", address),
				slog.String("err", err.Error()),
			)
			return uuid.Nil, fmt.Errorf("fdb get address: %w", err)
		}
		tr.Cancel()
		if len(value) == 0 {
			return uuid.Nil, nil
		}
		decodedID, err := uuid.FromBytes(value)
		if err != nil {
			log.ErrorContext(ctx, "node.address.decode",
				slog.String("node_type", nodeType),
				slog.String("address_kind", string(addressKind)),
				slog.String("address", address),
				slog.String("err", err.Error()),
			)
			return uuid.Nil, fmt.Errorf("decode address owner: %w", err)
		}
		return decodedID, nil
	}
}

// WriteAddress registers a node type address value to a node ID.
func (s *NodeStore) WriteAddress(ctx context.Context, nodeType string, addressKind node.AddressKind, address string, nodeID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.write_address")(&err)
	log := telemetry.L(ctx)
	addressKey := fdb.Key(addressIndexKey(nodeType, addressKind, address))
	idBytes, _ := nodeID.MarshalBinary()
	for {
		tr, err := s.db.CreateTransaction()
		if err != nil {
			log.ErrorContext(ctx, "node.address.transaction.create", slog.String("err", err.Error()))
			return fmt.Errorf("create address write transaction: %w", err)
		}
		if err := ensureAddressOwner(ctx, tr, addressKey, nodeType, address, nodeID); err != nil {
			retry, retryErr := retryAddressTransaction(ctx, tr, "node.address.owner.retry", err)
			tr.Cancel()
			if retryErr != nil {
				return retryErr
			}
			if retry {
				continue
			}
			return err
		}
		tr.Set(addressKey, idBytes)
		if err := tr.Commit().Get(); err != nil {
			retry, retryErr := retryAddressTransaction(ctx, tr, "node.address.write.retry", err)
			if retryErr != nil {
				return retryErr
			}
			if retry {
				continue
			}
			log.ErrorContext(ctx, "node.address.write",
				slog.String("node_type", nodeType),
				slog.String("address_kind", string(addressKind)),
				slog.String("address", address),
				slog.String("node_id", nodeID.String()),
				slog.String("err", err.Error()),
			)
			return fmt.Errorf("fdb write address: %w", err)
		}
		return nil
	}
}

func ensureAddressOwner(ctx context.Context, tr fdb.Transaction, key fdb.Key, nodeType, address string, nodeID uuid.UUID) error {
	log := telemetry.L(ctx)
	existing, err := tr.Get(key).Get()
	if err != nil {
		log.ErrorContext(ctx, "node.address.owner.read",
			slog.String("node_type", nodeType),
			slog.String("address", address),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("read existing address: %w", err)
	}
	if len(existing) == 0 {
		return nil
	}
	existingID, err := uuid.FromBytes(existing)
	if err != nil {
		log.ErrorContext(ctx, "node.address.owner.decode",
			slog.String("node_type", nodeType),
			slog.String("address", address),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("decode existing address owner: %w", err)
	}
	if existingID == nodeID {
		return nil
	}
	log.ErrorContext(ctx, "node.address.owner.conflict",
		slog.String("node_type", nodeType),
		slog.String("address", address),
		slog.String("existing_node_id", existingID.String()),
		slog.String("err", domain.ErrAlreadyExists.Error()),
	)
	return fmt.Errorf("address %q (type %q) already owned by %s: %w", address, nodeType, existingID, domain.ErrAlreadyExists)
}

// DeleteAddress removes a node type address value.
func (s *NodeStore) DeleteAddress(ctx context.Context, nodeType string, addressKind node.AddressKind, address string) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.delete_address")(&err)
	log := telemetry.L(ctx)
	addressKey := fdb.Key(addressIndexKey(nodeType, addressKind, address))
	for {
		tr, err := s.db.CreateTransaction()
		if err != nil {
			log.ErrorContext(ctx, "node.address.transaction.create", slog.String("err", err.Error()))
			return fmt.Errorf("create address delete transaction: %w", err)
		}
		tr.Clear(addressKey)
		if err := tr.Commit().Get(); err != nil {
			retry, retryErr := retryAddressTransaction(ctx, tr, "node.address.delete.retry", err)
			if retryErr != nil {
				return retryErr
			}
			if retry {
				continue
			}
			log.ErrorContext(ctx, "node.address.delete",
				slog.String("node_type", nodeType),
				slog.String("address_kind", string(addressKind)),
				slog.String("address", address),
				slog.String("err", err.Error()),
			)
			return fmt.Errorf("fdb delete address: %w", err)
		}
		return nil
	}
}

func retryAddressTransaction(ctx context.Context, tr fdb.Transaction, operation string, err error) (bool, error) {
	var fdbErr fdb.Error
	if !errors.As(err, &fdbErr) {
		return false, nil
	}
	if retryErr := tr.OnError(fdbErr).Get(); retryErr != nil {
		log := telemetry.L(ctx)
		log.ErrorContext(ctx, operation, slog.String("err", retryErr.Error()))
		return false, fmt.Errorf("%s: %w", operation, retryErr)
	}
	return true, nil
}
