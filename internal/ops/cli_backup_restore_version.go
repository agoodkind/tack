package ops

import (
	"bytes"
	"context"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

const restoreVersionPayloadFormat = "text/plain; charset=utf-8"

// backupFDBRestoreVersionCommand builds the restore-version leaf: it converts
// a wall-clock moment into the FoundationDB version a restore names, reading
// only the object store, because the cluster that would otherwise convert the
// moment is what a total loss destroyed (TACK-468).
func backupFDBRestoreVersionCommand(factory *cli.Factory) *cobra.Command {
	var requestedAt string

	cmd := &cobra.Command{
		Use:   "restore-version",
		Short: "Convert a moment into the FoundationDB version to restore to, from the object store alone",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&requestedAt, "at", "",
		"the moment to restore to, as RFC 3339 (2026-08-30T01:07:23Z) or 2026/08/30.01:07:23+0000 (default: now)")
	clispec.AttachRawRead(cmd, factory,
		singletonReadSpec(audit.VerbOpsBackupFDBRestoreVersion, "backup/restore-version"), emptyCommandInput,
		func(ctx context.Context) (audit.OperatorReadOutput, error) {
			want := opsNow().UTC()

			if requestedAt != "" {
				parsed, err := parseFDBTargetTime(requestedAt)
				if err != nil {
					return audit.OperatorReadOutput{}, err
				}

				want = parsed
			}

			var answer bytes.Buffer

			err := RunBackupFDBRestoreVersion(ctx, factory.Cfg, want, &answer)
			if err != nil {
				return audit.OperatorReadOutput{}, err
			}

			return audit.OperatorReadOutput{Payload: answer.Bytes(), Format: restoreVersionPayloadFormat, Targets: nil}, nil
		})

	return cmd
}
