package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type repairClassEntry struct {
	Class       RepairClass `json:"class"`
	Description string      `json:"description"`
}

func repairClassesOp(f *cli.Factory) clispec.Operation[noInput] {
	_ = f
	return clispec.Operation[noInput]{
		Name:  clispec.Name{Canonical: "classes"},
		Group: repairGroup,
		Short: "List supported repair classes",
		New:   func() noInput { return noInput{} },
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			classes := make([]repairClassEntry, 0)
			for _, info := range RepairClasses() {
				classes = append(classes, repairClassEntry{Class: info.Class, Description: info.Description})
			}
			return sink.Emit(ctx, map[string]any{"command": "repair.classes", "classes": classes})
		},
	}
}

type repairPreviewInput struct {
	clispec.InputMarker
	NodeID   string
	Class    string
	Profile  string
	Manifest string
}

func repairPreviewOp(f *cli.Factory) clispec.Operation[repairPreviewInput] {
	return clispec.Operation[repairPreviewInput]{
		Name:  clispec.Name{Canonical: "preview"},
		Group: repairGroup,
		Short: "Preview one node with --profile or many nodes with --manifest",
		Params: []clispec.Param[repairPreviewInput]{
			clispec.StringParam("node", "node UUID", "", false, func(in *repairPreviewInput, v string) { in.NodeID = v }),
			clispec.StringParam("class", "repair class", string(DefaultRepairClass()), false, func(in *repairPreviewInput, v string) { in.Class = v }),
			clispec.StringParam("profile", "repair profile JSON file", "", false, func(in *repairPreviewInput, v string) { in.Profile = v }),
			clispec.StringParam("manifest", "repair manifest JSON file", "", false, func(in *repairPreviewInput, v string) { in.Manifest = v }),
		},
		New: func() repairPreviewInput { return repairPreviewInput{Class: string(DefaultRepairClass())} },
		Run: func(ctx context.Context, in repairPreviewInput, sink clispec.ResultSink) error {
			repairClass, err := parseRepairClass("repair preview", in.Class)
			if err != nil {
				return err
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			console := NewRepairConsoleFromEnv(env)
			if strings.TrimSpace(in.Manifest) != "" {
				out, manifestErr := previewRepairManifest(ctx, console, repairClass, in.Manifest)
				if manifestErr != nil {
					return manifestErr
				}
				return sink.Emit(ctx, out)
			}
			nodeID, err := uuid.Parse(strings.TrimSpace(in.NodeID))
			if err != nil {
				return fmt.Errorf("repair preview: --node or --manifest is required: %w", err)
			}
			profile, err := readRepairProfileArg(ctx, "repair preview", in.Profile)
			if err != nil {
				return err
			}
			preview, err := console.Preview(ctx, RepairPreviewInput{Class: repairClass, NodeID: nodeID, Profile: profile})
			if err != nil {
				return fmt.Errorf("repair preview: repair for %s: %w", nodeID, err)
			}
			out := previewOutput{
				Command:     "repair.preview",
				RepairClass: repairClass,
				Status:      repairPreviewStatus(preview),
				Summary:     preview.Summary,
				Preview:     preview,
				SafeMode:    "preview never writes; apply requires --confirm and --yes",
			}
			if preview.CanApply && preview.ConfirmationToken != "" {
				out.ApplyCommand = fmt.Sprintf("./server ops repair apply --profile %s --class %s --node %s --actor <actor-uuid> --confirm %s --yes", in.Profile, repairClass, nodeID, preview.ConfirmationToken)
			}
			return sink.Emit(ctx, out)
		},
	}
}

type repairApplyInput struct {
	clispec.InputMarker
	NodeID   string
	ActorID  string
	Class    string
	Profile  string
	Manifest string
	Confirm  string
	Yes      bool
}

func repairApplyOp(f *cli.Factory) clispec.Operation[repairApplyInput] {
	return clispec.Operation[repairApplyInput]{
		Name:  clispec.Name{Canonical: "apply"},
		Group: repairGroup,
		Short: "Apply a previewed repair after explicit confirmation",
		Params: []clispec.Param[repairApplyInput]{
			clispec.StringParam("node", "node UUID", "", false, func(in *repairApplyInput, v string) { in.NodeID = v }),
			clispec.StringParam("actor", "actor UUID", "", true, func(in *repairApplyInput, v string) { in.ActorID = v }),
			clispec.StringParam("class", "repair class", string(DefaultRepairClass()), false, func(in *repairApplyInput, v string) { in.Class = v }),
			clispec.StringParam("profile", "repair profile JSON file", "", false, func(in *repairApplyInput, v string) { in.Profile = v }),
			clispec.StringParam("manifest", "repair manifest JSON file", "", false, func(in *repairApplyInput, v string) { in.Manifest = v }),
			clispec.StringParam("confirm", "preview confirmation token", "", false, func(in *repairApplyInput, v string) { in.Confirm = v }),
			clispec.BoolParam("yes", "confirm that this command may write", false, func(in *repairApplyInput, v bool) { in.Yes = v }),
		},
		New: func() repairApplyInput { return repairApplyInput{Class: string(DefaultRepairClass())} },
		Run: func(ctx context.Context, in repairApplyInput, sink clispec.ResultSink) error {
			if !in.Yes {
				return fmt.Errorf("repair apply: --yes is required because this command writes persisted state")
			}
			actorID, err := uuid.Parse(strings.TrimSpace(in.ActorID))
			if err != nil {
				return fmt.Errorf("repair apply: --actor must be a UUID: %w", err)
			}
			repairClass, err := parseRepairClass("repair apply", in.Class)
			if err != nil {
				return err
			}
			env, err := NewEnv(ctx, f.Cfg)
			if err != nil {
				return err
			}
			defer env.Close()
			console := NewRepairConsoleFromEnv(env)
			if strings.TrimSpace(in.Manifest) != "" {
				out, manifestErr := applyRepairManifest(ctx, console, repairClass, in.Manifest, actorID)
				if manifestErr != nil {
					return manifestErr
				}
				return sink.Emit(ctx, out)
			}
			if strings.TrimSpace(in.NodeID) == "" || strings.TrimSpace(in.Confirm) == "" {
				return fmt.Errorf("repair apply: --node and --confirm are required unless --manifest is used")
			}
			nodeID, err := uuid.Parse(strings.TrimSpace(in.NodeID))
			if err != nil {
				return fmt.Errorf("repair apply: --node must be a UUID: %w", err)
			}
			profile, err := readRepairProfileArg(ctx, "repair apply", in.Profile)
			if err != nil {
				return err
			}
			result, err := console.Apply(ctx, RepairApplyInput{ActorID: actorID, Class: repairClass, ConfirmationToken: in.Confirm, NodeID: nodeID, Profile: profile})
			if err != nil {
				return fmt.Errorf("repair apply: repair for %s: %w", nodeID, err)
			}
			return sink.Emit(ctx, repairApplyOutput{
				Command:     "repair.apply",
				RepairClass: repairClass,
				Status:      "applied",
				SafeMode:    "write completed only after matching preview token and explicit --yes",
				Result:      result,
			})
		},
	}
}
