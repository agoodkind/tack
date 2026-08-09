package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// parseRepairClass parses a repair class for a CLI command, returning an error
// rather than exiting so the work function can surface it.
func parseRepairClass(raw string) (RepairClass, error) {
	repairClass, err := ParseRepairClass(raw)
	if err != nil {
		return RepairClass(""), err
	}
	return repairClass, nil
}

// readRepairProfileArg reads a required repair profile file for a command.
func readRepairProfileArg(ctx context.Context, command, path string) (*RepairReferenceProfile, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New(command + ": --profile is required")
	}
	profile, err := readRepairProfile(ctx, path)
	if err != nil {
		return nil, err
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
		Name:     clispec.Name{Canonical: "node", CLIOverride: ""},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsVerifyNode), Reads: true},
		Group:    verifyGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Check node consistency and constraints",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[verifyNodeInput]{
			clispec.StringParam("node", "node UUID", "", true, func(in *verifyNodeInput, v string) { in.NodeID = v }),
			clispec.StringParam("class", "optional repair class", "", false, func(in *verifyNodeInput, v string) { in.Class = v }),
			clispec.StringParam("profile", "repair profile JSON file", "", false, func(in *verifyNodeInput, v string) { in.Profile = v }),
			clispec.BoolParam("records", "include raw inspection rows", false, func(in *verifyNodeInput, v bool) { in.Records = v }),
		},
		New: func() verifyNodeInput {
			return verifyNodeInput{InputMarker: clispec.InputMarker{}, NodeID: "", Class: "", Profile: "", Records: false}
		},
		Run: func(ctx context.Context, in verifyNodeInput, sink clispec.ResultSink) error {
			nodeID, err := uuid.Parse(strings.TrimSpace(in.NodeID))
			if err != nil {
				slog.ErrorContext(ctx, "verify.node_bad_node", slog.String("err", err.Error()))
				return fmt.Errorf("verify node: --node must be a UUID: %w", err)
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			report, err := env.Stores.Inspect.QueryNodeRecords(ctx, nodeID)
			if err != nil {
				slog.ErrorContext(ctx, "verify.node_query_failed", slog.String("err", err.Error()))
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
			result := repairVerifyResult{
				ResultMarker: clispec.ResultMarker{},
				Command:      "verify.node",
				NodeID:       nodeID,
				RepairClass:  RepairClass(""),
				Status:       status,
				Checks:       checks,
				Warnings:     selectedNode.Warnings,
				Records:      nil,
			}
			if strings.TrimSpace(in.Class) != "" {
				repairClass, classErr := parseRepairClass(in.Class)
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
			return clispec.WriteJSONValue(ctx, sink, result)
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
		Name:     clispec.Name{Canonical: "node", CLIOverride: ""},
		Audit:    audit.Spec{Verb: string(audit.VerbOpsValidateNode), Reads: true},
		Group:    validateGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Check repair applicability for a node",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[validateNodeInput]{
			clispec.StringParam("node", "node UUID", "", true, func(in *validateNodeInput, v string) { in.NodeID = v }),
			clispec.StringParam("class", "repair class", string(DefaultRepairClass()), false, func(in *validateNodeInput, v string) { in.Class = v }),
			clispec.StringParam("profile", "repair profile JSON file", "", false, func(in *validateNodeInput, v string) { in.Profile = v }),
		},
		New: func() validateNodeInput {
			return validateNodeInput{InputMarker: clispec.InputMarker{}, NodeID: "", Class: string(DefaultRepairClass()), Profile: ""}
		},
		Run: func(ctx context.Context, in validateNodeInput, sink clispec.ResultSink) error {
			nodeID, err := uuid.Parse(strings.TrimSpace(in.NodeID))
			if err != nil {
				slog.ErrorContext(ctx, "validate.node_bad_node", slog.String("err", err.Error()))
				return fmt.Errorf("validate node: --node must be a UUID: %w", err)
			}
			repairClass, err := parseRepairClass(in.Class)
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
				slog.ErrorContext(ctx, "validate.node_preview_failed", slog.String("err", err.Error()))
				return fmt.Errorf("validate node: preview repair for %s: %w", nodeID, err)
			}
			checks := []repairCheck{
				{Name: "repair_class_supported", OK: true, Details: string(repairClass)},
				{Name: "node_requires_repair", OK: preview.NeedsRepair, Details: repairNeedDetails(preview)},
				{Name: "repair_can_apply", OK: !preview.NeedsRepair || preview.CanApply, Details: preview.Summary},
			}
			result := repairValidateResult{
				ResultMarker: clispec.ResultMarker{},
				Command:      "validate.node",
				NodeID:       nodeID.String(),
				RepairClass:  repairClass,
				Status:       repairPreviewStatus(preview),
				Summary:      preview.Summary,
				Checks:       checks,
				Preview:      preview,
			}
			return clispec.WriteJSONValue(ctx, sink, result)
		},
	}
}
