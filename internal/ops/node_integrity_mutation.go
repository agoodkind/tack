package ops

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

type repairPhaseRun struct {
	manifestDigest string
	operator       audit.OperatorPrincipal
	entry          NodeIntegrityEntry
	stored         *node.RepairCheckpoint
}

// checkpoint returns the checkpoint write that sets the stored phase to
// phase. The write preserves the comment and export fields of the stored
// checkpoint.
func (run repairPhaseRun) checkpoint(phase node.RepairPhase) node.CheckpointWrite {
	write := node.CheckpointWrite{
		Previous: "",
		Next: node.RepairCheckpoint{
			RepairID: run.entry.RepairID, ManifestDigest: run.manifestDigest, NodeID: run.entry.NodeID,
			SourceDigest: run.entry.Source.PrimaryDigest, Phase: phase, ActorID: run.operator.ID, UpdatedAt: clock.Now().UTC(),
		},
	}
	if run.stored != nil {
		write.Previous = run.stored.Phase
		write.Next.CommentID, write.Next.ContentDigest = run.stored.CommentID, run.stored.ContentDigest
		write.Next.ExportPath, write.Next.ExportDigest = run.stored.ExportPath, run.stored.ExportDigest
	}
	return write
}

// repairFrozen contains the values that each node change of one entry reads
// from the frozen source.
type repairFrozen struct {
	orgID         uuid.UUID
	primaryDigest node.Digest
	metadataEpoch int64
}

func frozenRepairSource(ctx context.Context, entry NodeIntegrityEntry) (repairFrozen, error) {
	frozen := repairFrozen{orgID: uuid.Nil, primaryDigest: node.Digest{}, metadataEpoch: 0}
	var primary node.Node
	if err := json.Unmarshal(entry.Source.Primary, &primary); err != nil {
		return frozen, repairRefused(ctx, entry, "the frozen primary record is not a node record", domain.ErrInvalidArgument)
	}
	digest, err := decodeRepairDigest(entry.Source.PrimaryDigest)
	if err != nil {
		return frozen, repairRefused(ctx, entry, err.Error(), domain.ErrInvalidArgument)
	}
	epochText, found := strings.CutPrefix(entry.Source.MetadataEpoch, primary.OrgID.String()+"=")
	epoch, parseErr := strconv.ParseInt(epochText, 10, 64)
	if !found || parseErr != nil {
		return frozen, repairRefused(ctx, entry, fmt.Sprintf("metadata epoch %q is not an epoch of organization %s", entry.Source.MetadataEpoch, primary.OrgID), domain.ErrInvalidArgument)
	}
	frozen.orgID, frozen.primaryDigest, frozen.metadataEpoch = primary.OrgID, digest, epoch
	return frozen, nil
}

func decodeRepairDigest(text string) (node.Digest, error) {
	var digest node.Digest
	decoded, err := hex.DecodeString(text)
	if err != nil || len(decoded) != len(digest) {
		return digest, fmt.Errorf("digest %q is not a hex SHA-256 digest", text)
	}
	copy(digest[:], decoded)
	return digest, nil
}

// readRepairSnapshot reads the mutation snapshot of the repaired node. It
// requires the snapshot primary digest and metadata epoch to equal the
// frozen values and returns an error that wraps [domain.ErrConflict]
// otherwise.
func readRepairSnapshot(ctx context.Context, mutator node.Mutator, entry NodeIntegrityEntry, frozen repairFrozen) (node.MutationSnapshot, node.SchemaSnapshot, error) {
	snapshot, schema, err := readNodeSnapshot(ctx, mutator, entry, frozen.orgID, entry.NodeID)
	if err != nil {
		return snapshot, schema, err
	}
	if !snapshot.Exists || snapshot.PrimaryDigest != frozen.primaryDigest {
		return snapshot, schema, repairRefused(ctx, entry, "the current primary record differs from the frozen source", domain.ErrConflict)
	}
	if schema.MetadataEpoch != frozen.metadataEpoch {
		return snapshot, schema, repairRefused(ctx, entry, "the metadata epoch changed after the freeze", domain.ErrConflict)
	}
	return snapshot, schema, nil
}

func readNodeSnapshot(ctx context.Context, mutator node.Mutator, entry NodeIntegrityEntry, orgID, nodeID uuid.UUID) (node.MutationSnapshot, node.SchemaSnapshot, error) {
	snapshot, schema, err := mutator.ReadMutationSnapshot(ctx, orgID, nodeID)
	if err != nil {
		wrapped := fmt.Errorf("read the mutation snapshot of node %s for repair %s: %w", nodeID, entry.RepairID, err)
		slog.ErrorContext(ctx, "repair.snapshot.read_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", entry.RepairID))
		return snapshot, schema, wrapped
	}
	return snapshot, schema, nil
}

// frozenGraphRecords returns the frozen primary digests of the resulting
// parent chain and the added edge targets in node ID order. It omits a
// node that the freeze did not find in the organization.
func frozenGraphRecords(ctx context.Context, entry NodeIntegrityEntry) ([]node.GraphRecord, error) {
	wanted := map[uuid.UUID]bool{}
	for _, nodeID := range entry.Source.Chain {
		wanted[nodeID] = true
	}
	for _, edge := range entry.Result.AddEdges {
		wanted[edge.TargetID] = true
	}
	records := []node.GraphRecord{}
	for _, related := range entry.Source.Related {
		if !wanted[related.NodeID] || related.PrimaryDigest == "" {
			continue
		}
		digest, err := decodeRepairDigest(related.PrimaryDigest)
		if err != nil {
			return nil, repairRefused(ctx, entry, err.Error(), domain.ErrInvalidArgument)
		}
		records = append(records, node.GraphRecord{NodeID: related.NodeID, PrimaryDigest: digest})
	}
	sortGraphRecords(records)
	return records, nil
}

func sortGraphRecords(records []node.GraphRecord) {
	slices.SortFunc(records, func(left, right node.GraphRecord) int {
		return strings.Compare(left.NodeID.String(), right.NodeID.String())
	})
}

func repairRefused(ctx context.Context, entry NodeIntegrityEntry, reason string, sentinel error) error {
	wrapped := fmt.Errorf("repair %s for node %s: %s: %w", entry.RepairID, entry.NodeID, reason, sentinel)
	slog.ErrorContext(ctx, "repair.refused", slog.String("err", wrapped.Error()),
		slog.String("repair_id", entry.RepairID), slog.String("node_id", entry.NodeID.String()))
	return wrapped
}

func repairMutationFailure(ctx context.Context, entry NodeIntegrityEntry, operation string, err error) error {
	wrapped := fmt.Errorf("%s for repair %s of node %s: %w", operation, entry.RepairID, entry.NodeID, err)
	slog.ErrorContext(ctx, "repair.mutation_failed", slog.String("err", wrapped.Error()),
		slog.String("repair_id", entry.RepairID), slog.String("operation", operation))
	return wrapped
}
