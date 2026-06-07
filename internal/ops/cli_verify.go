package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// parseRepairClass parses a repair class for a CLI command, returning an error
// rather than exiting so the work function can surface it.
func parseRepairClass(command, raw string) (RepairClass, error) {
	repairClass, err := ParseRepairClass(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", command, err)
	}
	return repairClass, nil
}

// readRepairProfileArg reads a required repair profile file for a command.
func readRepairProfileArg(ctx context.Context, command, path string) (*RepairReferenceProfile, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%s: --profile is required", command)
	}
	profile, err := readRepairProfile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", command, err)
	}
	return profile, nil
}

type verifyNodeInput struct {
	clispec.InputMarker
	NodeID  string
	Class   string
	Profile string
	Records bool
}

func verifyNodeOp(f *cli.Factory) clispec.Operation[verifyNodeInput] {
	return clispec.Operation[verifyNodeInput]{
		Name:  clispec.Name{Canonical: "node"},
		Group: verifyGroup,
		Short: "Check node consistency and constraints",
		Params: []clispec.Param[verifyNodeInput]{
			clispec.StringParam("node", "node UUID", "", true, func(in *verifyNodeInput, v string) { in.NodeID = v }),
			clispec.StringParam("class", "optional repair class", "", false, func(in *verifyNodeInput, v string) { in.Class = v }),
			clispec.StringParam("profile", "repair profile JSON file", "", false, func(in *verifyNodeInput, v string) { in.Profile = v }),
			clispec.BoolParam("records", "include raw inspection rows", false, func(in *verifyNodeInput, v bool) { in.Records = v }),
		},
		New: func() verifyNodeInput { return verifyNodeInput{} },
		Run: func(ctx context.Context, in verifyNodeInput, sink clispec.ResultSink) error {
			nodeID, err := uuid.Parse(strings.TrimSpace(in.NodeID))
			if err != nil {
				return fmt.Errorf("verify node: --node must be a UUID: %w", err)
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
			if err != nil {
				return fmt.Errorf("verify node: inspect node %s: %w", nodeID, err)
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
			result := repairVerifyResult{Command: "verify.node", NodeID: nodeID, Status: status, Checks: checks, Warnings: selectedNode.Warnings}
			if strings.TrimSpace(in.Class) != "" {
				repairClass, classErr := parseRepairClass("verify node", in.Class)
				if classErr != nil {
					return classErr
				}
				profile, profErr := readRepairProfileArg(ctx, "verify node", in.Profile)
				if profErr != nil {
					return profErr
				}
				result.RepairClass = repairClass
				preview, previewErr := NewRepairConsoleFromEnv(env).Preview(ctx, RepairPreviewInput{Class: repairClass, NodeID: nodeID, Profile: profile})
				if previewErr != nil {
					result.Status = "failed"
					result.Checks = append(result.Checks, repairCheck{Name: "repair_preview", OK: false, Details: previewErr.Error()})
				} else {
					result.Checks = append(result.Checks, repairCheck{Name: "repair_preview", OK: true, Details: repairPreviewStatus(preview) + ": " + preview.Summary})
				}
			}
			if in.Records {
				result.Records = report
			}
			return sink.Emit(ctx, result)
		},
	}
}

type validateNodeInput struct {
	clispec.InputMarker
	NodeID  string
	Class   string
	Profile string
}

func validateNodeOp(f *cli.Factory) clispec.Operation[validateNodeInput] {
	return clispec.Operation[validateNodeInput]{
		Name:  clispec.Name{Canonical: "node"},
		Group: validateGroup,
		Short: "Check repair applicability for a node",
		Params: []clispec.Param[validateNodeInput]{
			clispec.StringParam("node", "node UUID", "", true, func(in *validateNodeInput, v string) { in.NodeID = v }),
			clispec.StringParam("class", "repair class", string(DefaultRepairClass()), false, func(in *validateNodeInput, v string) { in.Class = v }),
			clispec.StringParam("profile", "repair profile JSON file", "", false, func(in *validateNodeInput, v string) { in.Profile = v }),
		},
		New: func() validateNodeInput { return validateNodeInput{Class: string(DefaultRepairClass())} },
		Run: func(ctx context.Context, in validateNodeInput, sink clispec.ResultSink) error {
			nodeID, err := uuid.Parse(strings.TrimSpace(in.NodeID))
			if err != nil {
				return fmt.Errorf("validate node: --node must be a UUID: %w", err)
			}
			repairClass, err := parseRepairClass("validate node", in.Class)
			if err != nil {
				return err
			}
			profile, err := readRepairProfileArg(ctx, "validate node", in.Profile)
			if err != nil {
				return err
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			preview, err := NewRepairConsoleFromEnv(env).Preview(ctx, RepairPreviewInput{Class: repairClass, NodeID: nodeID, Profile: profile})
			if err != nil {
				return fmt.Errorf("validate node: preview repair for %s: %w", nodeID, err)
			}
			checks := []repairCheck{
				{Name: "repair_class_supported", OK: true, Details: string(repairClass)},
				{Name: "node_requires_repair", OK: preview.NeedsRepair, Details: repairNeedDetails(preview)},
				{Name: "repair_can_apply", OK: !preview.NeedsRepair || preview.CanApply, Details: preview.Summary},
			}
			return sink.Emit(ctx, repairValidateResult{Command: "validate.node", NodeID: nodeID.String(), RepairClass: repairClass, Status: repairPreviewStatus(preview), Summary: preview.Summary, Checks: checks, Preview: preview})
		},
	}
}
