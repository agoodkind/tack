package ops

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

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

func TestRevokeRecordsRequestedTokenID(t *testing.T) {
	dsn := testenv.Ledger(t)
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)

	for _, tokenID := range []string{uuid.Must(uuid.NewV7()).String(), "invalid-bearer-example"} {
		t.Run(
			tokenID,
			func(t *testing.T) {
				operatorID := uuid.Must(uuid.NewV7())
				factory := cli.System(&config.Config{DatabaseURL: dsn})
				var output bytes.Buffer
				factory.Out, factory.Err = &output, &output
				factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
				registry := clispec.NewRegistry()
				clispec.Register(registry, authTokenRevokeOp(factory))
				root := &cobra.Command{Use: "server", SilenceErrors: true, SilenceUsage: true}
				factory.RegisterGlobalFlags(root)
				root.AddCommand(clispec.RenderCobra(registry, factory)...)
				factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
				root.SetArgs([]string{
					"--execute",
					"--operator-id",
					operatorID.String(),
					"--operator-email",
					"revoke@example.test",
					"ops",
					"auth",
					"token-revoke",
					"--id",
					tokenID,
				})
				commandErr := root.ExecuteContext(t.Context())
				if commandErr == nil {
					t.Fatal("revoking a missing or invalid token succeeded")
				}

				_, parseErr := uuid.Parse(tokenID)
				if parseErr == nil &&
					!strings.Contains(commandErr.Error(), "no token "+tokenID+" exists") {
					t.Fatalf("revocation error = %v, want a missing-token error", commandErr)
				}

				if parseErr != nil && !strings.Contains(
					commandErr.Error(),
					"--id is not a token id",
				) {
					t.Fatalf("revocation error = %v, want an invalid-ID error", commandErr)
				}

				events := opsoutbox.Events(
					t,
					pool,
					opsoutbox.Filter{
						Verb:  audit.VerbOpsAuthTokenRevoke,
						Path:  []string{"actor", "id"},
						Value: operatorID.String(),
					},
				)
				if len(events) == 0 {
					t.Fatal("revocation did not record the attempt")
				}

				for _, event := range events {
					var extra struct {
						Input map[string]string `json:"input"`
					}
					if err := json.Unmarshal(event.Extra, &extra); err != nil {
						t.Fatal(err)
					}

					if parseErr == nil &&
						(len(extra.Input) != 1 || extra.Input["token_id"] != tokenID) {
						t.Fatalf(
							"recorded input = %+v, want only the requested token ID",
							extra.Input,
						)
					}

					if parseErr != nil &&
						(len(extra.Input) != 0 || strings.Contains(string(event.Extra), tokenID)) {
						t.Fatal("revocation recorded the invalid value")
					}
				}
			},
		)
	}
}
