package clispec

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

const (
	// jsonPreviewFormat is the payload format of a preview that writes one
	// JSON document.
	jsonPreviewFormat = "application/json"
	// textPreviewFormat is the payload format of a preview that writes text
	// or more than one document.
	textPreviewFormat = "text/plain; charset=utf-8"
	// commandTargetPrefix starts the resource URI of a command preview that
	// does not declare its own targets.
	commandTargetPrefix = "tack:command/"
)

// Preview reads system state for a dry run and writes the planned work to
// sink. It returns the identities of the objects it read, in order.
type Preview func(ctx context.Context, sink ResultSink) ([]audit.Target, error)

// RunRecordedPreview runs preview through [RunAuditedRead] with spec and
// captures its output in memory. It writes that output to the command sink
// of f after RunAuditedRead succeeds.
//
// The normal read lifecycle saves the final event and output digest before
// replay. The infrastructure fallback in RunAuditedRead can succeed without
// that evidence and still permit replay.
//
//   - A preview or audit error prevents replay.
//   - Replay preserves the original writes and their order.
func RunRecordedPreview(
	ctx context.Context,
	f *cli.Factory,
	spec audit.Spec,
	input json.RawMessage,
	preview Preview,
) error {
	captured := &captureSink{writes: nil}
	_, err := RunAuditedRead(
		ctx,
		f,
		spec,
		input,
		func(ctx context.Context) (audit.OperatorReadOutput, error) {
			targets, err := preview(ctx, captured)
			if err != nil {
				return audit.OperatorReadOutput{}, err
			}

			return captured.output(targets), nil
		},
	)
	if err != nil {
		return err
	}

	return captured.replay(ctx, NewCLISink(f))
}

// PreviewSpec returns the read definition for the dry run of spec.
//
// The definition preserves the verb and CreatesAuditInfrastructure flag.
//
//   - When spec provides both Targets and SafeInput, the preview uses both.
//   - Otherwise, the preview targets tack:command/<verb> and does not
//     record input.
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

// withoutInput does not include input in the audit event.
func withoutInput(json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

type capturedWrite struct {
	payload []byte
	isJSON  bool
}

// captureSink stores preview writes in order until [RunRecordedPreview]
// replays them after [RunAuditedRead] succeeds.
type captureSink struct {
	writes []capturedWrite
}

func (s *captureSink) WriteJSON(_ context.Context, payload json.RawMessage) error {
	s.writes = append(s.writes, capturedWrite{payload: bytes.Clone(payload), isJSON: true})
	return nil
}

func (s *captureSink) WriteText(_ context.Context, body string) error {
	s.writes = append(s.writes, capturedWrite{payload: []byte(body), isJSON: false})
	return nil
}

// output returns the captured bytes, joined with newlines, as the read
// payload in the format that previewFormat selects.
func (s *captureSink) output(targets []audit.Target) audit.OperatorReadOutput {
	payloads := make([][]byte, 0, len(s.writes))
	for _, write := range s.writes {
		payloads = append(payloads, write.payload)
	}

	return audit.OperatorReadOutput{
		Payload: bytes.Join(payloads, []byte("\n")),
		Format:  previewFormat(s.writes),
		Targets: targets,
	}
}

// previewFormat returns the JSON format for exactly one JSON write and the
// text format for any other list of writes.
func previewFormat(writes []capturedWrite) string {
	if len(writes) == 1 && writes[0].isJSON {
		return jsonPreviewFormat
	}

	return textPreviewFormat
}

// replay writes the captured writes to sink in their original order.
func (s *captureSink) replay(ctx context.Context, sink ResultSink) error {
	for _, write := range s.writes {
		var err error
		if write.isJSON {
			err = sink.WriteJSON(ctx, write.payload)
		} else {
			err = sink.WriteText(ctx, string(write.payload))
		}

		if err != nil {
			return loggedAuditError(ctx, "write preview output", err)
		}
	}

	return nil
}
