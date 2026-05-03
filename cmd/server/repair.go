package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
)

const repairPrefix = "repair."

type repairSelection struct {
	RepairName string     `json:"repair_name"`
	NodeID     *uuid.UUID `json:"node_id,omitempty"`
	OrgID      *uuid.UUID `json:"org_id,omitempty"`
}

func runRepair(cfg *config.Config, args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "list" {
		printRepairUsage()
		return
	}
	switch args[0] {
	case "read":
		runRepairRead(cfg, args[1:])
	case "find":
		runRepairFind(cfg, args[1:])
	case "query":
		runRepairQuery(cfg, args[1:])
	case "verify":
		runRepairVerify(cfg, args[1:])
	case "validate":
		runRepairValidate(args[1:])
	case "preview":
		runRepairPreview(args[1:])
	case "apply":
		runRepairApply(cfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown repair command: %s\n\n", args[0])
		printRepairUsage()
		os.Exit(2)
	}
}

func runRepairValidate(argv []string) {
	selection := parseRepairSelection("validate", argv)
	writeRepairJSON(map[string]any{
		"command":                "validate",
		"repair":                 selection.RepairName,
		"node_id":                selection.NodeID,
		"org_id":                 selection.OrgID,
		"repair_exists":          true,
		"scoped_apply_supported": false,
	})
}

func runRepairPreview(argv []string) {
	selection := parseRepairSelection("preview", argv)
	writeRepairJSON(map[string]any{
		"command":                 "preview",
		"repair":                  selection.RepairName,
		"node_id":                 selection.NodeID,
		"org_id":                  selection.OrgID,
		"scoped_apply_supported":  false,
		"preview_execution_ready": false,
		"note":                    "registered repair ops currently execute as whole-operation CLI runs",
	})
}

func runRepairApply(cfg *config.Config, argv []string) {
	selection := parseRepairSelection("apply", argv)
	if selection.NodeID != nil || selection.OrgID != nil {
		fmt.Fprintln(os.Stderr, "apply: --node and --org are not supported by registered repair ops")
		os.Exit(2)
	}
	if err := ops.Run(context.Background(), cfg, selection.RepairName); err != nil {
		slog.Error("repair.apply", "repair", selection.RepairName, "err", err)
		os.Exit(1)
	}
}

func parseRepairSelection(command string, argv []string) repairSelection {
	flagSet := newRepairFlagSet(command)
	repairName := flagSet.String("repair", "", "repair op name")
	nodeIDRaw := flagSet.String("node", "", "node UUID scope")
	orgIDRaw := flagSet.String("org", "", "org UUID scope")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *repairName == "" {
		fmt.Fprintf(os.Stderr, "%s: --repair is required\n", command)
		os.Exit(2)
	}
	if *nodeIDRaw != "" && *orgIDRaw != "" {
		fmt.Fprintf(os.Stderr, "%s: --node and --org are mutually exclusive\n", command)
		os.Exit(2)
	}
	normalizedName := normalizeRepairName(*repairName)
	if _, ok := ops.Get(normalizedName); !ok || !strings.HasPrefix(normalizedName, repairPrefix) {
		fmt.Fprintf(os.Stderr, "%s: unknown repair op %q\n", command, *repairName)
		printRepairOperations()
		os.Exit(2)
	}
	selection := repairSelection{RepairName: normalizedName}
	if *nodeIDRaw != "" {
		nodeID := mustParseUUID(command, "--node", *nodeIDRaw)
		selection.NodeID = &nodeID
	}
	if *orgIDRaw != "" {
		orgID := mustParseUUID(command, "--org", *orgIDRaw)
		selection.OrgID = &orgID
	}
	return selection
}

func normalizeRepairName(rawName string) string {
	if strings.HasPrefix(rawName, repairPrefix) {
		return rawName
	}
	return repairPrefix + rawName
}

func mustOpenRepairEnv(cfg *config.Config) *ops.Env {
	env, err := ops.NewEnv(context.Background(), cfg)
	if err != nil {
		slog.Error("repair.env", "err", err)
		os.Exit(1)
	}
	return env
}

func mustParseUUID(command, flagName, rawValue string) uuid.UUID {
	parsedID, err := uuid.Parse(rawValue)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s must be a UUID\n", command, flagName)
		os.Exit(2)
	}
	return parsedID
}

func newRepairFlagSet(name string) *flag.FlagSet {
	flagSet := flag.NewFlagSet(name, flag.ContinueOnError)
	flagSet.SetOutput(os.Stderr)
	flagSet.Usage = func() {}
	return flagSet
}

func printRepairUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server repair <command> [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  read      Read one node, its resolve record, and its primary record")
	fmt.Fprintln(os.Stderr, "  find      List node views by org and optional type")
	fmt.Fprintln(os.Stderr, "  query     Filter node views by property equality")
	fmt.Fprintln(os.Stderr, "  verify    Verify resolve, view, and primary record presence")
	fmt.Fprintln(os.Stderr, "  validate  Validate repair op selection and scope flags")
	fmt.Fprintln(os.Stderr, "  preview   Preview repair op execution metadata")
	fmt.Fprintln(os.Stderr, "  apply     Run a registered repair op")
	fmt.Fprintln(os.Stderr)
	printRepairOperations()
}

func printRepairOperations() {
	fmt.Fprintln(os.Stderr, "available repair ops:")
	for _, operation := range ops.List() {
		if !strings.HasPrefix(operation.Name, repairPrefix) {
			continue
		}
		fmt.Fprintf(os.Stderr, "  %-32s %s\n", operation.Name, operation.Description)
	}
}

func writeRepairJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}
