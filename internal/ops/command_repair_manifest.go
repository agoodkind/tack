package ops

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
)

func runRepairManifestPreview(ctx context.Context, cfg *config.Config, repairClass RepairClass, manifestPath string) {
	manifest, err := readRepairManifest(ctx, manifestPath)
	if err != nil {
		failCommandf("repair preview: %v", err)
	}
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	console := NewRepairConsoleFromEnv(env)
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
	writeCommandJSON(RepairManifestPreviewOutput{
		Command:     "repair.preview.manifest",
		RepairClass: repairClass,
		Profile:     manifest.Profile,
		Results:     results,
		SafeMode:    "manifest preview never writes; apply requires confirmation tokens and --yes",
	})
}

func runRepairManifestApply(ctx context.Context, cfg *config.Config, repairClass RepairClass, manifestPath string, actorID uuid.UUID) {
	manifest, err := readRepairManifest(ctx, manifestPath)
	if err != nil {
		failCommandf("repair apply: %v", err)
	}
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	console := NewRepairConsoleFromEnv(env)
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
	writeCommandJSON(RepairManifestApplyOutput{
		Command:     "repair.apply.manifest",
		RepairClass: repairClass,
		Profile:     manifest.Profile,
		Results:     results,
		SafeMode:    "write completed only after matching preview tokens and explicit --yes",
	})
}

func mustReadRepairProfileArg(ctx context.Context, command string, path string) *RepairReferenceProfile {
	if strings.TrimSpace(path) == "" {
		failCommandUsage(command, "%s: --profile is required", command)
	}
	profile, err := readRepairProfile(ctx, path)
	if err != nil {
		failCommandf("%s: %v", command, err)
	}
	return profile
}
