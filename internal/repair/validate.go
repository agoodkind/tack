package repair

import (
	"context"
	"fmt"
	"os"
	"strings"

	"goodkind.io/tack/internal/config"
)

type repairCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Details string `json:"details,omitempty"`
}

type repairValidateResult struct {
	Command     string         `json:"command"`
	NodeID      string         `json:"node_id"`
	RepairClass RepairClass    `json:"repair_class"`
	Status      string         `json:"status"`
	Summary     string         `json:"summary"`
	Checks      []repairCheck  `json:"checks"`
	Preview     *RepairPreview `json:"preview"`
}

func RunValidate(cfg *config.Config, argv []string) {
	runValidate(cfg, argv)
}

func runValidate(cfg *config.Config, argv []string) {
	flagSet := newFlagSet("validate")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	classRaw := flagSet.String("class", string(DefaultRepairClass()), "repair class")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if flagSet.NArg() != 0 {
		failUsage("validate", "validate: unexpected positional arguments")
	}
	if strings.TrimSpace(*nodeIDRaw) == "" {
		failUsage("validate", "validate: --node is required")
	}
	nodeID := mustParseUUID("validate", "--node", *nodeIDRaw)
	repairClass := mustParseRepairClass("validate", *classRaw)
	env := mustOpenEnv(cfg)
	defer env.Close()
	console := newRepairConsoleFromEnv(env)
	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: repairClass, NodeID: nodeID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate: preview repair for %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	checks := []repairCheck{
		{Name: "repair_class_supported", OK: true, Details: string(repairClass)},
		{Name: "node_requires_repair", OK: preview.NeedsRepair, Details: repairNeedDetails(preview)},
		{Name: "repair_can_apply", OK: !preview.NeedsRepair || preview.CanApply, Details: preview.Summary},
	}
	writeJSON(repairValidateResult{
		Command:     "validate",
		NodeID:      nodeID.String(),
		RepairClass: repairClass,
		Status:      repairPreviewStatus(preview),
		Summary:     preview.Summary,
		Checks:      checks,
		Preview:     preview,
	})
}
