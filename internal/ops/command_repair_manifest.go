package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clispec"
)

// previewRepairManifest previews every node in a manifest under one repair
// class and returns the aggregated output.
func previewRepairManifest(ctx context.Context, console *RepairConsole, repairClass RepairClass, manifestPath string) (RepairManifestPreviewOutput, error) {
	manifest, err := readRepairManifest(ctx, manifestPath)
	if err != nil {
		slog.ErrorContext(ctx, "repair.preview_manifest_read_failed", slog.String("err", err.Error()))
		return RepairManifestPreviewOutput{}, fmt.Errorf("repair preview: %w", err)
	}
	results := make([]RepairManifestPreview, 0, len(manifest.Nodes))
	for _, entry := range manifest.Nodes {
		preview, previewErr := console.Preview(ctx, RepairPreviewInput{Class: repairClass, NodeID: entry.NodeID, Profile: &manifest.Profile})
		result := RepairManifestPreview{NodeID: entry.NodeID, Status: "", Summary: "", Preview: nil, Error: ""}
		if previewErr != nil {
			result.Status = "error"
			result.Error = previewErr.Error()
		} else {
			result.Status = repairPreviewStatus(preview)
			result.Summary = preview.Summary
			result.Preview = preview
		}
		results = append(results, result)
	}
	return RepairManifestPreviewOutput{
		ResultMarker: clispec.ResultMarker{},
		Command:      "repair.preview.manifest",
		RepairClass:  repairClass,
		Profile:      manifest.Profile,
		Results:      results,
		SafeMode:     "manifest preview never writes; apply requires confirmation tokens and --yes",
	}, nil
}

// applyRepairManifest applies every node in a manifest under one repair class,
// skipping entries without a confirmation token, and returns the output.
func applyRepairManifest(ctx context.Context, console *RepairConsole, repairClass RepairClass, manifestPath string, actorID uuid.UUID) (RepairManifestApplyOutput, error) {
	manifest, err := readRepairManifest(ctx, manifestPath)
	if err != nil {
		slog.ErrorContext(ctx, "repair.apply_manifest_read_failed", slog.String("err", err.Error()))
		return RepairManifestApplyOutput{}, fmt.Errorf("repair apply: %w", err)
	}
	results := make([]RepairManifestApply, 0, len(manifest.Nodes))
	for _, entry := range manifest.Nodes {
		result := RepairManifestApply{NodeID: entry.NodeID, Status: "", Result: nil, Error: ""}
		if strings.TrimSpace(entry.ConfirmationToken) == "" {
			result.Status = "skipped"
			result.Error = "confirmation_token is required"
			results = append(results, result)
			continue
		}
		applyResult, applyErr := console.Apply(ctx, RepairApplyInput{ActorID: actorID, Class: repairClass, ConfirmationToken: entry.ConfirmationToken, NodeID: entry.NodeID, Profile: &manifest.Profile})
		if applyErr != nil {
			result.Status = "error"
			result.Error = applyErr.Error()
		} else {
			result.Status = "applied"
			result.Result = applyResult
		}
		results = append(results, result)
	}
	return RepairManifestApplyOutput{
		ResultMarker: clispec.ResultMarker{},
		Command:      "repair.apply.manifest",
		RepairClass:  repairClass,
		Profile:      manifest.Profile,
		Results:      results,
		SafeMode:     "write completed only after matching preview tokens and explicit --yes",
	}, nil
}
