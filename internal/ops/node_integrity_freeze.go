package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

func freezeNodeIntegrityRequest(ctx context.Context, reader RepairEvidenceReader, request NodeIntegrityRequest) (NodeIntegrityManifest, error) {
	manifest := NodeIntegrityManifest{
		Version: request.Version, Ticket: request.Ticket, Digest: "",
		Entries: make([]NodeIntegrityEntry, 0, len(request.Entries)),
	}
	if err := checkNodeIntegrityRequest(request); err != nil {
		return manifest, repairManifestRejected(ctx, "freeze", err)
	}
	for _, entry := range request.Entries {
		evidence, err := readRepairEvidence(ctx, reader, entry)
		if err != nil {
			return manifest, err
		}
		if problem := graphProblem(entry, evidence); problem != "" {
			return manifest, repairManifestRejected(ctx, "freeze", fmt.Errorf("repair %s for node %s: %s", entry.RepairID, entry.NodeID, problem))
		}
		source, err := sourceFromEvidence(ctx, evidence)
		if err != nil {
			return manifest, err
		}
		entry.Source = &source
		manifest.Entries = append(manifest.Entries, entry)
	}
	digest, err := nodeIntegrityManifestDigest(ctx, manifest)
	if err != nil {
		return manifest, err
	}
	manifest.Digest = digest
	telemetry.L(ctx).InfoContext(ctx, "repair.manifest.frozen",
		slog.String("manifest_digest", digest), slog.Int("entries", len(manifest.Entries)))
	return manifest, nil
}

func repairManifestRejected(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("%s node integrity manifest: %w", operation, err)
	slog.ErrorContext(ctx, "repair.manifest.rejected", slog.String("err", wrapped.Error()), slog.String("operation", operation))
	return wrapped
}
