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

type repairSelection struct {
	NodeID *uuid.UUID `json:"node_id,omitempty"`
	OrgID  *uuid.UUID `json:"org_id,omitempty"`
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
		runRepairValidate(cfg, args[1:])
	case "preview":
		runRepairPreview(cfg, args[1:])
	case "apply":
		runRepairApply(cfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown repair command: %s\n\n", args[0])
		printRepairUsage()
		os.Exit(2)
	}
}

func runRepairValidate(cfg *config.Config, argv []string) {
	selection := parseRepairSelection("validate", argv)
	if selection.NodeID == nil {
		fmt.Fprintln(os.Stderr, "validate: --node is required")
		os.Exit(2)
	}
	env := mustOpenRepairEnv(cfg)
	defer env.Close()
	console := ops.NewRepairConsoleFromEnv(env)
	preview, err := console.Preview(context.Background(), ops.RepairPreviewInput{
		Class:  ops.RepairClassStrayAliasState,
		NodeID: *selection.NodeID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate: preview repair for %s: %v\n", selection.NodeID.String(), err)
		os.Exit(1)
	}
	writeRepairJSON(map[string]any{
		"command":                  "validate",
		"node_id":                  selection.NodeID,
		"repair_class":             ops.RepairClassStrayAliasState,
		"can_apply":                preview.CanApply,
		"needs_repair":             preview.NeedsRepair,
		"summary":                  preview.Summary,
		"raw_state":                preview.RawState,
		"canonical_state_id":       preview.CanonicalStateID,
		"resolved_raw_state_id":    preview.ResolvedRawStateID,
		"resolved_canonical_state": preview.ResolvedCanonicalStateID,
		"winner_state_id":          preview.WinnerStateID,
	})
}

func runRepairPreview(cfg *config.Config, argv []string) {
	selection := parseRepairSelection("preview", argv)
	if selection.NodeID == nil {
		fmt.Fprintln(os.Stderr, "preview: --node is required")
		os.Exit(2)
	}
	env := mustOpenRepairEnv(cfg)
	defer env.Close()
	console := ops.NewRepairConsoleFromEnv(env)
	preview, err := console.Preview(context.Background(), ops.RepairPreviewInput{
		Class:  ops.RepairClassStrayAliasState,
		NodeID: *selection.NodeID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "preview: repair for %s: %v\n", selection.NodeID.String(), err)
		os.Exit(1)
	}
	writeRepairJSON(map[string]any{
		"command":            "preview",
		"node_id":            selection.NodeID,
		"repair_class":       ops.RepairClassStrayAliasState,
		"preview":            preview,
		"confirmation_token": preview.ConfirmationToken,
	})
}

func runRepairApply(cfg *config.Config, argv []string) {
	flagSet := newRepairFlagSet("apply")
	nodeIDRaw := flagSet.String("node", "", "node UUID")
	actorIDRaw := flagSet.String("actor", "", "actor UUID")
	confirmationToken := flagSet.String("confirm", "", "preview confirmation token")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *nodeIDRaw == "" || *actorIDRaw == "" || strings.TrimSpace(*confirmationToken) == "" {
		fmt.Fprintln(os.Stderr, "usage: ./server repair apply --node <uuid> --actor <uuid> --confirm <token>")
		os.Exit(2)
	}
	nodeID := mustParseUUID("apply", "--node", *nodeIDRaw)
	actorID := mustParseUUID("apply", "--actor", *actorIDRaw)
	env := mustOpenRepairEnv(cfg)
	defer env.Close()
	console := ops.NewRepairConsoleFromEnv(env)
	result, err := console.Apply(context.Background(), ops.RepairApplyInput{
		ActorID:           actorID,
		Class:             ops.RepairClassStrayAliasState,
		ConfirmationToken: *confirmationToken,
		NodeID:            nodeID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: repair for %s: %v\n", nodeID, err)
		os.Exit(1)
	}
	writeRepairJSON(map[string]any{
		"command":      "apply",
		"repair_class": ops.RepairClassStrayAliasState,
		"result":       result,
	})
}

func parseRepairSelection(command string, argv []string) repairSelection {
	flagSet := newRepairFlagSet(command)
	nodeIDRaw := flagSet.String("node", "", "node UUID scope")
	orgIDRaw := flagSet.String("org", "", "org UUID scope")
	if err := flagSet.Parse(argv); err != nil {
		os.Exit(2)
	}
	if *nodeIDRaw != "" && *orgIDRaw != "" {
		fmt.Fprintf(os.Stderr, "%s: --node and --org are mutually exclusive\n", command)
		os.Exit(2)
	}
	selection := repairSelection{}
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
	fmt.Fprintln(os.Stderr, "  validate  Validate and summarize stray-alias repair viability")
	fmt.Fprintln(os.Stderr, "  preview   Preview stray-alias repair for one node")
	fmt.Fprintln(os.Stderr, "  apply     Apply stray-alias repair with actor and confirmation token")
}

func writeRepairJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}
