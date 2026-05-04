package repair

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
)

func mustOpenEnv(cfg *config.Config) *ops.Env {
	env, err := ops.NewEnv(context.Background(), cfg)
	if err != nil {
		slog.Error("repair.env", "err", err)
		os.Exit(1)
	}
	return env
}

func newRepairConsoleFromEnv(env *ops.Env) *RepairConsole {
	return NewRepairConsole(
		env.Stores.Nodes,
		env.Stores.Views,
		env.Stores.NodeTypes,
		env.Stores.PropertyDefs,
		NewSearcher(env.Cfg),
	)
}

func mustParseUUID(command string, flagName string, rawValue string) uuid.UUID {
	parsedID, err := uuid.Parse(rawValue)
	if err != nil {
		failUsage(command, "%s: %s must be a UUID", command, flagName)
	}
	return parsedID
}

func mustParseRepairClass(command string, rawValue string) RepairClass {
	repairClass, err := ParseRepairClass(rawValue)
	if err != nil {
		failUsage(command, "%s: %v", command, err)
	}
	return repairClass
}

func newFlagSet(name string) *flag.FlagSet {
	flagSet := flag.NewFlagSet(name, flag.ContinueOnError)
	flagSet.SetOutput(os.Stderr)
	flagSet.Usage = func() {}
	return flagSet
}

func failUsage(command string, message string, args ...any) {
	fmt.Fprintf(os.Stderr, message+"\n\n", args...)
	PrintCommandUsage(command)
	os.Exit(2)
}

func writeJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "repair: encode output: %v\n", err)
		os.Exit(1)
	}
}

func repairNeedDetails(preview *RepairPreview) string {
	if preview.NeedsRepair {
		return "repair would change persisted props"
	}
	return "node already matches canonical state storage"
}

func repairPreviewStatus(preview *RepairPreview) string {
	if preview == nil {
		return "blocked"
	}
	if !preview.NeedsRepair {
		return "noop"
	}
	if preview.CanApply {
		return "ready"
	}
	return "blocked"
}
