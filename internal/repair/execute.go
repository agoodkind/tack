package repair

import (
	"context"
	"fmt"
	"os"
	"strings"

	"goodkind.io/tack/internal/config"
)

type previewOutput struct {
	Command      string         `json:"command"`
	RepairClass  RepairClass    `json:"repair_class"`
	Status       string         `json:"status"`
	Summary      string         `json:"summary"`
	Preview      *RepairPreview `json:"preview"`
	ApplyCommand string         `json:"apply_command,omitempty"`
	SafeMode     string         `json:"safe_mode"`
}

func RunPreview(cfg *config.Config, argv []string) {
	flagSet := newFlagSet("preview")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	classRaw := flagSet.String("class", string(DefaultRepairClass()), "repair class")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if flagSet.NArg() != 0 {
		failUsage("preview", "preview: unexpected positional arguments")
	}
	if strings.TrimSpace(*nodeIDRaw) == "" {
		failUsage("preview", "preview: --node is required")
	}
	nodeID := mustParseUUID("preview", "--node", *nodeIDRaw)
	repairClass := mustParseRepairClass("preview", *classRaw)
	env := mustOpenEnv(cfg)
	defer env.Close()
	console := newRepairConsoleFromEnv(env)
	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: repairClass, NodeID: nodeID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "preview: repair for %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	result := previewOutput{
		Command:     "preview",
		RepairClass: repairClass,
		Status:      repairPreviewStatus(preview),
		Summary:     preview.Summary,
		Preview:     preview,
		SafeMode:    "preview never writes; apply requires --confirm and --yes",
	}
	if preview.CanApply && preview.ConfirmationToken != "" {
		result.ApplyCommand = fmt.Sprintf("./server repair apply --class %s --node %s --actor <actor-uuid> --confirm %s --yes", repairClass, nodeID, preview.ConfirmationToken)
	}
	writeJSON(result)
}

func RunApply(cfg *config.Config, argv []string) {
	flagSet := newFlagSet("apply")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	actorIDRaw := flagSet.String("actor", "", "actor UUID")
	classRaw := flagSet.String("class", string(DefaultRepairClass()), "repair class")
	confirmationToken := flagSet.String("confirm", "", "preview confirmation token")
	confirmed := flagSet.Bool("yes", false, "confirm that this command may write")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if flagSet.NArg() != 0 {
		failUsage("apply", "apply: unexpected positional arguments")
	}
	if strings.TrimSpace(*nodeIDRaw) == "" || strings.TrimSpace(*actorIDRaw) == "" || strings.TrimSpace(*confirmationToken) == "" {
		failUsage("apply", "apply: --node, --actor, and --confirm are required")
	}
	if !*confirmed {
		failUsage("apply", "apply: --yes is required because this command writes persisted state")
	}
	nodeID := mustParseUUID("apply", "--node", *nodeIDRaw)
	actorID := mustParseUUID("apply", "--actor", *actorIDRaw)
	repairClass := mustParseRepairClass("apply", *classRaw)
	env := mustOpenEnv(cfg)
	defer env.Close()
	console := newRepairConsoleFromEnv(env)
	result, err := console.Apply(context.Background(), RepairApplyInput{
		ActorID:           actorID,
		Class:             repairClass,
		ConfirmationToken: *confirmationToken,
		NodeID:            nodeID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: repair for %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	writeJSON(map[string]any{
		"command":      "apply",
		"repair_class": repairClass,
		"status":       "applied",
		"safe_mode":    "write completed only after matching preview token and explicit --yes",
		"result":       result,
	})
}
