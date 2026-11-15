package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

func TestExportRecordsRequestedSelection(t *testing.T) {
	dsn := testenv.Ledger(t)
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)

	for _, name := range []string{"export", "alias", "missing-key"} {
		t.Run(
			name,
			func(t *testing.T) {
				operatorID, orgID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
				keyPath := writeVerifyTestKey(t, t.TempDir())
				if name == "missing-key" {
					keyPath = filepath.Join(t.TempDir(), "missing.pem")
				}

				factory := cli.System(&config.Config{
					AuditReaderDSN:      dsn,
					AuditSigningKeyPath: keyPath,
				})
				var output bytes.Buffer
				factory.Out, factory.Err = &output, &output
				factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
				registry := clispec.NewRegistry()
				registerAudit(registry, factory)
				root := &cobra.Command{Use: "server", SilenceErrors: true, SilenceUsage: true}
				factory.RegisterGlobalFlags(root)
				root.AddCommand(clispec.RenderCobra(registry, factory)...)
				factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
				selection := audit.ExportSelection{
					OrgID:     orgID.String(),
					Oldest:    time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
					Latest:    time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
					RequestID: "selected-request",
					TraceID:   "selected-trace",
					Limit:     7,
				}
				command := []string{"audit", "export"}
				if name == "alias" {
					command = []string{"audit-export"}
				}

				args := []string{
					"--execute",
					"--operator-id",
					operatorID.String(),
					"--operator-email",
					"export@example.test",
				}
				args = append(args, command...)
				args = append(
					args,
					"--org",
					selection.OrgID,
					"--oldest",
					selection.Oldest,
					"--latest",
					selection.Latest,
					"--request-id",
					selection.RequestID,
					"--trace-id",
					selection.TraceID,
					"--limit",
					"7",
					"--out",
					filepath.Join(t.TempDir(), "bundle"),
				)
				root.SetArgs(args)
				commandErr := root.ExecuteContext(t.Context())
				if name == "missing-key" {
					if !errors.Is(commandErr, fs.ErrNotExist) {
						t.Fatalf("export error = %v, want missing signing key", commandErr)
					}
				} else if commandErr != nil {
					t.Fatalf("export failed: %v", commandErr)
				}

				events := opsoutbox.Events(
					t,
					pool,
					opsoutbox.Filter{
						Verb:  audit.VerbAuditExport,
						Path:  []string{"actor", "id"},
						Value: operatorID.String(),
					},
				)
				if len(events) == 0 {
					t.Fatal("the export did not record its requested selection")
				}

				for _, event := range events {
					var extra struct {
						Input audit.ExportSelection `json:"input"`
					}
					if err := json.Unmarshal(event.Extra, &extra); err != nil {
						t.Fatal(err)
					}

					if extra.Input != selection {
						t.Fatalf("recorded selection = %+v, want %+v", extra.Input, selection)
					}

					var fields map[string]json.RawMessage
					var input struct {
						Input json.RawMessage `json:"input"`
					}
					if err := json.Unmarshal(event.Extra, &input); err != nil {
						t.Fatal(err)
					}

					if err := json.Unmarshal(input.Input, &fields); err != nil {
						t.Fatal(err)
					}

					if len(fields) != 6 {
						t.Fatalf(
							"selection has %d fields, want only the six requested filters",
							len(fields),
						)
					}
				}
			},
		)
	}
}
