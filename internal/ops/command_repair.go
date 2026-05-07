package ops

import (
	"context"
	"fmt"
	"strings"

	"goodkind.io/tack/internal/config"
)

func runVerifyNode(ctx context.Context, cfg *config.Config, argv []string) {
	flagSet := newCommandFlagSet("verify node")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	classRaw := flagSet.String("class", "", "optional repair class")
	profilePath := flagSet.String("profile", "", "repair profile JSON file")
	includeRecords := flagSet.Bool("records", false, "include raw inspection rows")
	err := flagSet.Parse(argv)
	if err != nil {
		exitCommand(2)
	}
	if flagSet.NArg() != 0 {
		failCommandUsage("verify node", "verify node: unexpected positional arguments")
	}
	if strings.TrimSpace(*nodeIDRaw) == "" {
		failCommandUsage("verify node", "verify node: --node is required")
	}
	nodeID := mustParseUUIDArg("verify node", "--node", *nodeIDRaw)
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
	if err != nil {
		failCommandf("verify node: inspect node %s: %v", nodeID, err)
	}
	selectedNode := selectRepairNode(report)
	checks := repairInspectionChecks(selectedNode, report)
	status := "ok"
	for _, check := range checks {
		if !check.OK {
			status = "failed"
			break
		}
	}
	result := repairVerifyResult{
		Command:     "verify.node",
		NodeID:      nodeID,
		RepairClass: "",
		Status:      status,
		Checks:      checks,
		Warnings:    selectedNode.Warnings,
		Records:     nil,
	}
	if strings.TrimSpace(*classRaw) != "" {
		repairClass := mustParseRepairClassArg("verify node", *classRaw)
		profile := mustReadRepairProfileArg(ctx, "verify node", *profilePath)
		result.RepairClass = repairClass
		console := NewRepairConsoleFromEnv(env)
		preview, previewErr := console.Preview(ctx, RepairPreviewInput{Class: repairClass, NodeID: nodeID, Profile: profile})
		if previewErr != nil {
			result.Status = "failed"
			result.Checks = append(result.Checks, repairCheck{Name: "repair_preview", OK: false, Details: previewErr.Error()})
		} else {
			result.Checks = append(result.Checks, repairCheck{Name: "repair_preview", OK: true, Details: repairPreviewStatus(preview) + ": " + preview.Summary})
		}
	}
	if *includeRecords {
		result.Records = report
	}
	writeCommandJSON(result)
}

func runValidateNode(ctx context.Context, cfg *config.Config, argv []string) {
	flagSet := newCommandFlagSet("validate node")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	classRaw := flagSet.String("class", string(DefaultRepairClass()), "repair class")
	profilePath := flagSet.String("profile", "", "repair profile JSON file")
	err := flagSet.Parse(argv)
	if err != nil {
		exitCommand(2)
	}
	if flagSet.NArg() != 0 {
		failCommandUsage("validate node", "validate node: unexpected positional arguments")
	}
	if strings.TrimSpace(*nodeIDRaw) == "" {
		failCommandUsage("validate node", "validate node: --node is required")
	}
	nodeID := mustParseUUIDArg("validate node", "--node", *nodeIDRaw)
	repairClass := mustParseRepairClassArg("validate node", *classRaw)
	profile := mustReadRepairProfileArg(ctx, "validate node", *profilePath)
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	console := NewRepairConsoleFromEnv(env)
	preview, err := console.Preview(ctx, RepairPreviewInput{Class: repairClass, NodeID: nodeID, Profile: profile})
	if err != nil {
		failCommandf("validate node: preview repair for %s: %v", nodeID, err)
	}
	checks := []repairCheck{
		{Name: "repair_class_supported", OK: true, Details: string(repairClass)},
		{Name: "node_requires_repair", OK: preview.NeedsRepair, Details: repairNeedDetails(preview)},
		{Name: "repair_can_apply", OK: !preview.NeedsRepair || preview.CanApply, Details: preview.Summary},
	}
	writeCommandJSON(repairValidateResult{Command: "validate.node", NodeID: nodeID.String(), RepairClass: repairClass, Status: repairPreviewStatus(preview), Summary: preview.Summary, Checks: checks, Preview: preview})
}

func runRepairPreviewCommand(ctx context.Context, cfg *config.Config, argv []string) {
	flagSet := newCommandFlagSet("repair preview")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	classRaw := flagSet.String("class", string(DefaultRepairClass()), "repair class")
	profilePath := flagSet.String("profile", "", "repair profile JSON file")
	manifestPath := flagSet.String("manifest", "", "repair manifest JSON file")
	err := flagSet.Parse(argv)
	if err != nil {
		exitCommand(2)
	}
	if flagSet.NArg() != 0 {
		failCommandUsage("repair preview", "repair preview: unexpected positional arguments")
	}
	repairClass := mustParseRepairClassArg("repair preview", *classRaw)
	if strings.TrimSpace(*manifestPath) != "" {
		runRepairManifestPreview(ctx, cfg, repairClass, *manifestPath)
		return
	}
	if strings.TrimSpace(*nodeIDRaw) == "" {
		failCommandUsage("repair preview", "repair preview: --node or --manifest is required")
	}
	nodeID := mustParseUUIDArg("repair preview", "--node", *nodeIDRaw)
	profile := mustReadRepairProfileArg(ctx, "repair preview", *profilePath)
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	console := NewRepairConsoleFromEnv(env)
	preview, err := console.Preview(ctx, RepairPreviewInput{Class: repairClass, NodeID: nodeID, Profile: profile})
	if err != nil {
		failCommandf("repair preview: repair for %s: %v", nodeID, err)
	}
	result := previewOutput{
		Command:      "repair.preview",
		RepairClass:  repairClass,
		Status:       repairPreviewStatus(preview),
		Summary:      preview.Summary,
		Preview:      preview,
		ApplyCommand: "",
		SafeMode:     "preview never writes; apply requires --confirm and --yes",
	}
	if preview.CanApply && preview.ConfirmationToken != "" {
		result.ApplyCommand = fmt.Sprintf("./server ops repair apply --profile %s --class %s --node %s --actor <actor-uuid> --confirm %s --yes", *profilePath, repairClass, nodeID, preview.ConfirmationToken)
	}
	writeCommandJSON(result)
}

func runRepairApplyCommand(ctx context.Context, cfg *config.Config, argv []string) {
	flagSet := newCommandFlagSet("repair apply")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	actorIDRaw := flagSet.String("actor", "", "actor UUID")
	classRaw := flagSet.String("class", string(DefaultRepairClass()), "repair class")
	profilePath := flagSet.String("profile", "", "repair profile JSON file")
	manifestPath := flagSet.String("manifest", "", "repair manifest JSON file")
	confirmationToken := flagSet.String("confirm", "", "preview confirmation token")
	confirmed := flagSet.Bool("yes", false, "confirm that this command may write")
	err := flagSet.Parse(argv)
	if err != nil {
		exitCommand(2)
	}
	if flagSet.NArg() != 0 {
		failCommandUsage("repair apply", "repair apply: unexpected positional arguments")
	}
	if strings.TrimSpace(*actorIDRaw) == "" {
		failCommandUsage("repair apply", "repair apply: --actor is required")
	}
	if !*confirmed {
		failCommandUsage("repair apply", "repair apply: --yes is required because this command writes persisted state")
	}
	actorID := mustParseUUIDArg("repair apply", "--actor", *actorIDRaw)
	repairClass := mustParseRepairClassArg("repair apply", *classRaw)
	if strings.TrimSpace(*manifestPath) != "" {
		runRepairManifestApply(ctx, cfg, repairClass, *manifestPath, actorID)
		return
	}
	if strings.TrimSpace(*nodeIDRaw) == "" || strings.TrimSpace(*confirmationToken) == "" {
		failCommandUsage("repair apply", "repair apply: --node and --confirm are required unless --manifest is used")
	}
	nodeID := mustParseUUIDArg("repair apply", "--node", *nodeIDRaw)
	profile := mustReadRepairProfileArg(ctx, "repair apply", *profilePath)
	env := mustOpenCommandEnv(ctx, cfg)
	defer env.Close()
	console := NewRepairConsoleFromEnv(env)
	result, err := console.Apply(ctx, RepairApplyInput{ActorID: actorID, Class: repairClass, ConfirmationToken: *confirmationToken, NodeID: nodeID, Profile: profile})
	if err != nil {
		failCommandf("repair apply: repair for %s: %v", nodeID, err)
	}
	writeCommandJSON(repairApplyOutput{
		Command:     "repair.apply",
		RepairClass: repairClass,
		Status:      "applied",
		SafeMode:    "write completed only after matching preview token and explicit --yes",
		Result:      result,
	})
}
