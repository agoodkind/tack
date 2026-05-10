package ops

import (
	"context"
	"fmt"
	"os"
	"strings"

	"goodkind.io/tack/internal/config"
)

// commandFamily is the named enum of top-level ops command families. Bare
// string switches with many cases trip the staticcheck-extra enum gate.
type commandFamily string

const (
	familyInspect  commandFamily = "inspect"
	familyVerify   commandFamily = "verify"
	familyValidate commandFamily = "validate"
	familyRepair   commandFamily = "repair"
	familyBatch    commandFamily = "batch"
	familyAudit    commandFamily = "audit"
)

// RunCommand routes the unified operator CLI command family.
func RunCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 {
		printOpsUsage()
		return nil
	}
	if args[0] == "help" {
		if len(args) == 1 {
			printOpsUsage()
			return nil
		}
		printFamilyUsage(args[1])
		return nil
	}
	switch commandFamily(args[0]) {
	case familyInspect:
		return runInspectCommand(ctx, cfg, args[1:])
	case familyVerify:
		return runVerifyCommand(ctx, cfg, args[1:])
	case familyValidate:
		return runValidateCommand(ctx, cfg, args[1:])
	case familyRepair:
		return runRepairCommand(ctx, cfg, args[1:])
	case familyBatch:
		return runBatchCommand(ctx, cfg, args[1:])
	case familyAudit:
		return runAuditCommand(ctx, cfg, args[1:])
	}
	if _, ok := Get(args[0]); ok {
		return Run(ctx, cfg, args[0])
	}
	printOpsUsage()
	return fmt.Errorf("unknown ops command or batch operation %q", args[0])
}

func printFamilyUsage(family string) {
	switch commandFamily(strings.TrimSpace(family)) {
	case familyInspect:
		printInspectUsage()
	case familyVerify:
		printVerifyUsage()
	case familyValidate:
		printValidateUsage()
	case familyRepair:
		printRepairUsage()
	case familyBatch:
		printBatchUsage()
	case familyAudit:
		printAuditUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown ops family: %s\n\n", family)
		printOpsUsage()
		os.Exit(2)
	}
}

func runInspectCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printInspectUsage()
		return nil
	}
	switch args[0] {
	case "read":
		runInspectRead(ctx, cfg, args[1:])
		return nil
	case "find":
		runInspectFind(ctx, cfg, args[1:])
		return nil
	case "query":
		runInspectQuery(ctx, cfg, args[1:])
		return nil
	default:
		printInspectCommandUsage(args[0])
		return fmt.Errorf("unknown inspect command %q", args[0])
	}
}

func runVerifyCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printVerifyUsage()
		return nil
	}
	switch args[0] {
	case "node":
		runVerifyNode(ctx, cfg, args[1:])
		return nil
	default:
		printVerifyUsage()
		return fmt.Errorf("unknown verify command %q", args[0])
	}
}

func runValidateCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printValidateUsage()
		return nil
	}
	switch args[0] {
	case "node":
		runValidateNode(ctx, cfg, args[1:])
		return nil
	default:
		printValidateUsage()
		return fmt.Errorf("unknown validate command %q", args[0])
	}
}

func runRepairCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printRepairUsage()
		return nil
	}
	switch args[0] {
	case "classes":
		PrintClasses()
		return nil
	case "preview":
		runRepairPreviewCommand(ctx, cfg, args[1:])
		return nil
	case "apply":
		runRepairApplyCommand(ctx, cfg, args[1:])
		return nil
	default:
		printRepairCommandUsage(args[0])
		return fmt.Errorf("unknown repair command %q", args[0])
	}
}

func runAuditCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printAuditUsage()
		return nil
	}
	switch args[0] {
	case "parity":
		return runAuditParity(ctx, cfg)
	default:
		printAuditUsage()
		return fmt.Errorf("unknown audit command %q", args[0])
	}
}

func runBatchCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printBatchUsage()
		return nil
	}
	name := args[0]
	if _, ok := Get(name); !ok {
		printBatchUsage()
		return fmt.Errorf("unknown batch op %q", name)
	}
	return Run(ctx, cfg, name)
}
