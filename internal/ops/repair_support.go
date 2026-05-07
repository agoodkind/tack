package ops

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
)

func mustOpenCommandEnv(ctx context.Context, cfg *config.Config) *Env {
	env, err := NewEnv(ctx, cfg)
	if err != nil {
		slog.Error("ops.env", "err", err)
		os.Exit(1)
	}
	return env
}

func mustParseUUIDArg(command string, flagName string, rawValue string) uuid.UUID {
	parsedID, err := uuid.Parse(rawValue)
	if err != nil {
		failCommandUsage(command, "%s: %s must be a UUID", command, flagName)
	}
	return parsedID
}

func mustParseRepairClassArg(command string, rawValue string) RepairClass {
	repairClass, err := ParseRepairClass(rawValue)
	if err != nil {
		failCommandUsage(command, "%s: %v", command, err)
	}
	return repairClass
}

func newCommandFlagSet(name string) *flag.FlagSet {
	flagSet := flag.NewFlagSet(name, flag.ContinueOnError)
	flagSet.SetOutput(os.Stderr)
	flagSet.Usage = func() {}
	return flagSet
}

func failCommandUsage(command string, message string, args ...any) {
	fmt.Fprintf(os.Stderr, message+"\n\n", args...)
	printCommandUsage(command)
	os.Exit(2)
}

func writeCommandJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	err := encoder.Encode(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ops: encode output: %v\n", err)
		os.Exit(1)
	}
}

func exitCommand(code int) {
	os.Exit(code)
}

func failCommandf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	exitCommand(1)
}

func repairNeedDetails(preview *RepairPreview) string {
	if preview.NeedsRepair {
		return "repair would change persisted props"
	}
	return "reference property already matches selected storage"
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
