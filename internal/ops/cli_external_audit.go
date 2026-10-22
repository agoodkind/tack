package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
)

// externalSpec returns an audit specification for an external operator command.
// The command uses reference as its external target. SafeInput returns the
// command input unchanged. Callers must include only flag values that do not
// contain credentials.
func externalSpec(verb audit.Verb, reference string) audit.Spec {
	target := externalTarget(reference)
	return audit.Spec{
		Verb: string(verb), Mutates: true, Class: audit.OperationExternal,
		Targets: func(context.Context, json.RawMessage) ([]audit.Target, error) {
			return []audit.Target{target}, nil
		},
		SafeInput: func(input json.RawMessage) (json.RawMessage, error) { return input, nil },
	}
}

// backupSpec returns an external audit specification for the backup subcommand
// identified by use.
func backupSpec(verb audit.Verb, use string) audit.Spec {
	return externalSpec(verb, "backup/"+use)
}

// commandInput is a typed command input that a recorded command stores.
type commandInput interface {
	isCommandInput()
}

type emptyInput struct{}

func (emptyInput) isCommandInput() {}

type restoreDrillInput struct {
	YBRunKey      string `json:"yb_run_key,omitempty"`
	FDBTargetTime string `json:"fdb_target_time,omitempty"`
}

func (restoreDrillInput) isCommandInput() {}

// emptyCommandInput returns the JSON input of a command without flags.
func emptyCommandInput() (json.RawMessage, error) {
	return encodeCommandInput(emptyInput{})
}

func encodeCommandInput[Input commandInput](input Input) (json.RawMessage, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		slog.Error("ops.command_input_encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode command input: %w", err)
	}
	return encoded, nil
}
