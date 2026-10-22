package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/migrations"
)

const (
	jsonPayloadFormat = "application/json"
	textPayloadFormat = "text/plain; charset=utf-8"
)

// migrateInput contains the inputs for a migration run. A zero flag value does
// not set a requirement.
type migrateInput struct {
	To         int `json:"to"`
	ExpectFrom int `json:"expect_from"`
}

func migrateSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbDatabaseMigrate), Mutates: true, Class: audit.OperationExternal,
		Targets: migrationTargets, SafeInput: safeMigrateInput,
	}
}

func migrationStatusSpec() audit.Spec {
	return audit.Spec{
		Verb: string(audit.VerbDatabaseMigrate), Reads: true, Class: audit.OperationRead,
		Targets: migrationTargets, SafeInput: safeMigrateInput,
	}
}

func migrationTargets(context.Context, json.RawMessage) ([]audit.Target, error) {
	return []audit.Target{schemastatus.MigrationsTarget()}, nil
}

func safeMigrateInput(input json.RawMessage) (json.RawMessage, error) {
	var decoded migrateInput
	if err := json.Unmarshal(input, &decoded); err != nil {
		slog.Error("migrate.input_decode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("decode migrate input: %w", err)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		slog.Error("migrate.input_encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode migrate input: %w", err)
	}
	return encoded, nil
}

// migrateCommand builds the migrate command. With --execute, the command
// applies migrations as an external operation. Without --execute, it uses
// RunAuditedRead to record the migration state read before printing the result.
func migrateCommand(f *cli.Factory) *cobra.Command {
	in := migrateInput{To: 0, ExpectFrom: 0}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Run database migrations against DATABASE_URL; without --execute, print the migration state",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().IntVar(&in.To, "to", 0, "apply migrations through this version only")
	cmd.Flags().IntVar(&in.ExpectFrom, "expect-from", 0, "refuse the run unless the database is at this migration version")
	input := func() (json.RawMessage, error) {
		encoded, err := json.Marshal(in)
		if err != nil {
			slog.Error("migrate.input_encode_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("migrate: encode the command input: %w", err)
		}
		return encoded, nil
	}
	clispec.AttachExternalPreview(cmd, f, migrateSpec(), input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runMigrations(ctx, f, in, steps)
		},
		func(ctx context.Context) error {
			encoded, err := input()
			if err != nil {
				return err
			}
			return printMigrationStatus(ctx, f, encoded)
		})
	return cmd
}

// runMigrations applies migrations after checking the version requirements
// and prints the applied version.
func runMigrations(ctx context.Context, f *cli.Factory, in migrateInput, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	guard := schemastatus.Guard{ExpectFrom: int64(in.ExpectFrom), To: int64(in.To)}
	applied, details, err := schemastatus.ApplyRecorded(ctx, f.Cfg.DatabaseURL, migrations.FS, guard, steps)
	if err != nil {
		slog.ErrorContext(ctx, "migrate.failed", slog.String("err", err.Error()))
		return details, fmt.Errorf("migrate: %w", err)
	}
	if err := clispec.NewCLISink(f).WriteText(ctx, fmt.Sprintf("migrations complete through version %d", applied)); err != nil {
		slog.ErrorContext(ctx, "migrate.output_failed", slog.String("err", err.Error()))
		return details, fmt.Errorf("migrate: write the result: %w", err)
	}
	return details, nil
}

func printMigrationStatus(ctx context.Context, f *cli.Factory, input json.RawMessage) error {
	jsonOutput := f.OutputFormat() == cli.FormatJSON
	payload, err := clispec.RunAuditedRead(ctx, f, migrationStatusSpec(), input, func(ctx context.Context) (audit.OperatorReadOutput, error) {
		return readMigrationStatus(ctx, f, jsonOutput)
	})
	if err != nil {
		slog.ErrorContext(ctx, "migrate.status_failed", slog.String("err", err.Error()))
		return fmt.Errorf("migrate: read the migration state: %w", err)
	}
	sink := clispec.NewCLISink(f)
	if jsonOutput {
		err = sink.WriteJSON(ctx, payload)
	} else {
		err = sink.WriteText(ctx, string(payload))
	}
	if err != nil {
		slog.ErrorContext(ctx, "migrate.status_output_failed", slog.String("err", err.Error()))
		return fmt.Errorf("migrate: write the migration state: %w", err)
	}
	return nil
}

// readMigrationStatus reads the migration state and returns the exact bytes
// that the command prints. It also returns one target for each embedded migration.
func readMigrationStatus(ctx context.Context, f *cli.Factory, jsonOutput bool) (audit.OperatorReadOutput, error) {
	status, err := schemastatus.Read(ctx, f.Cfg.DatabaseURL, migrations.FS)
	if err != nil {
		slog.ErrorContext(ctx, "migrate.status_failed", slog.String("err", err.Error()))
		return audit.OperatorReadOutput{}, fmt.Errorf("read the migration state: %w", err)
	}
	targets := schemastatus.MigrationTargets(status.Available)
	if !jsonOutput {
		return audit.OperatorReadOutput{Payload: []byte(status.Summary()), Format: textPayloadFormat, Targets: targets}, nil
	}
	body, err := json.Marshal(status)
	if err != nil {
		slog.ErrorContext(ctx, "migrate.status_encode_failed", slog.String("err", err.Error()))
		return audit.OperatorReadOutput{}, fmt.Errorf("encode the migration state at version %d: %w", status.AppliedVersion, err)
	}
	return audit.OperatorReadOutput{Payload: body, Format: jsonPayloadFormat, Targets: targets}, nil
}
