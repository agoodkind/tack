package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// repairIDPattern accepts repair IDs with 1 to 128 characters.
// The first character must be an ASCII letter or digit.
// Each later character must be an ASCII letter, digit, period, underscore,
// colon, or hyphen.
var repairIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// nodeIntegrityManifestDigest returns the SHA-256 digest of manifest's JSON
// encoding with Digest empty.
func nodeIntegrityManifestDigest(ctx context.Context, manifest NodeIntegrityManifest) (string, error) {
	manifest.Digest = ""
	encoded, err := json.Marshal(manifest)
	if err != nil {
		wrapped := fmt.Errorf("encode node integrity manifest: %w", err)
		slog.ErrorContext(ctx, "repair.manifest.encode_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// checkNodeIntegrityRequest validates the request header and entries and
// rejects entries that already include frozen source evidence.
func checkNodeIntegrityRequest(request NodeIntegrityRequest) error {
	if err := checkManifestHeader(request.Version, request.Ticket, len(request.Entries)); err != nil {
		return err
	}
	for index := range request.Entries {
		if request.Entries[index].Source != nil {
			return fmt.Errorf("request entry %s has source evidence; the freeze sets it", request.Entries[index].RepairID)
		}
	}
	return checkEntries(request.Entries)
}

// checkNodeIntegrityManifest validates the manifest header, digest, entries,
// and frozen source evidence.
func checkNodeIntegrityManifest(ctx context.Context, manifest NodeIntegrityManifest) error {
	if err := checkManifestHeader(manifest.Version, manifest.Ticket, len(manifest.Entries)); err != nil {
		return err
	}
	digest, err := nodeIntegrityManifestDigest(ctx, manifest)
	if err != nil {
		return err
	}
	if manifest.Digest != digest {
		return fmt.Errorf("manifest digest %q does not match its content digest %q", manifest.Digest, digest)
	}
	for index := range manifest.Entries {
		if err := checkFrozenSource(manifest.Entries[index]); err != nil {
			return err
		}
	}
	return checkEntries(manifest.Entries)
}

func checkManifestHeader(version int, ticket string, entries int) error {
	if version != nodeIntegrityManifestVersion {
		return fmt.Errorf("manifest version %d is not %d", version, nodeIntegrityManifestVersion)
	}
	if ticket != nodeIntegrityTicket {
		return fmt.Errorf("manifest ticket %q is not %s", ticket, nodeIntegrityTicket)
	}
	if entries == 0 {
		return errors.New("manifest has no entries")
	}
	return nil
}

func checkEntries(entries []NodeIntegrityEntry) error {
	repairIDs := make(map[string]bool, len(entries))
	nodeIDs := make(map[uuid.UUID]bool, len(entries))
	for index := range entries {
		entry := entries[index]
		if !repairIDPattern.MatchString(entry.RepairID) {
			return fmt.Errorf("entry %d repair_id %q is not a stable repair ID", index, entry.RepairID)
		}
		if repairIDs[entry.RepairID] {
			return fmt.Errorf("repair_id %s repeats", entry.RepairID)
		}
		repairIDs[entry.RepairID] = true
		if entry.NodeID == uuid.Nil {
			return fmt.Errorf("repair %s has no node_id", entry.RepairID)
		}
		if nodeIDs[entry.NodeID] {
			return fmt.Errorf("repair %s repeats node %s; combine its changes into one entry", entry.RepairID, entry.NodeID)
		}
		nodeIDs[entry.NodeID] = true
		if reason := entryProblem(entry); reason != "" {
			return fmt.Errorf("repair %s: %s", entry.RepairID, reason)
		}
	}
	return nil
}

func checkFrozenSource(entry NodeIntegrityEntry) error {
	source := entry.Source
	if source == nil {
		return fmt.Errorf("repair %s has no frozen source", entry.RepairID)
	}
	if source.PrimaryDigest != primaryDigest(source.Primary) {
		return fmt.Errorf("repair %s primary digest does not match its frozen primary record", entry.RepairID)
	}
	var primary node.Node
	if decodeErr := json.Unmarshal(source.Primary, &primary); decodeErr != nil {
		return fmt.Errorf("repair %s frozen primary record is not a node record: %s", entry.RepairID, decodeErr.Error())
	}
	if primary.ID != entry.NodeID || primary.NodeType != entry.NodeType {
		return fmt.Errorf("repair %s frozen primary record is node %s of type %q, not node %s of type %q",
			entry.RepairID, primary.ID, primary.NodeType, entry.NodeID, entry.NodeType)
	}
	if source.GraphDigest == "" || source.MetadataEpoch == "" {
		return fmt.Errorf("repair %s has no graph digest or metadata epoch", entry.RepairID)
	}
	return nil
}
