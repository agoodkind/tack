package clispec

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

const (
	// commandTargetPrefix starts the resource URI of a command preview that
	// does not declare its own targets.
	commandTargetPrefix = "tack:command/"
)

// Preview reads system state for a dry run and writes the planned work to
// sink.
type Preview func(ctx context.Context, sink ResultSink) error

// RunRecordedPreview records the access attempt before printing the preview.
func RunRecordedPreview(
	ctx context.Context,
	f *cli.Factory,
	spec audit.Spec,
	input json.RawMessage,
	preview Preview,
) error {
	_, err := RunAuditedRead(
		ctx,
		f,
		spec,
		input,
		func(ctx context.Context) (audit.OperatorReadOutput, error) {
			previewErr := preview(ctx, NewCLISink(f))
			return audit.OperatorReadOutput{}, previewErr
		},
	)

	return err
}

// PreviewSpec returns the read definition that records the dry run of the
// command that spec defines. The definition keeps the verb and the
// CreatesAuditInfrastructure flag of spec. It uses the targets and safe input
// of spec when spec declares both. Otherwise, it targets the command
// resource tack:command/<verb> and records no input.
func PreviewSpec(spec audit.Spec) audit.Spec {
	preview := audit.Spec{
		Verb:                       spec.Verb,
		Reads:                      true,
		Class:                      audit.OperationRead,
		Targets:                    commandTargets(spec.Verb),
		SafeInput:                  withoutInput,
		CreatesAuditInfrastructure: spec.CreatesAuditInfrastructure,
	}
	if spec.Targets != nil && spec.SafeInput != nil {
		preview.Targets, preview.SafeInput = spec.Targets, spec.SafeInput
	}

	return preview
}

// commandTargets returns a target extractor that selects the command
// resource of verb for every input.
func commandTargets(verb string) func(context.Context, json.RawMessage) ([]audit.Target, error) {
	target := audit.Target{
		Kind:         audit.TargetKindResource,
		NodeType:     "",
		Reference:    "",
		Name:         "",
		URI:          commandTargetPrefix + verb,
		RelationType: "",
		ID:           uuid.Nil,
		SourceID:     uuid.Nil,
		TargetID:     uuid.Nil,
	}

	return func(context.Context, json.RawMessage) ([]audit.Target, error) {
		return []audit.Target{target}, nil
	}
}

// withoutInput selects no input for the audit event.
func withoutInput(json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}
