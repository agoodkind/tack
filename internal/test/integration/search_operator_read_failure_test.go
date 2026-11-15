package integration_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchOperatorReadFailureBoundaries checks real admission and finalization
// refusals, command failures, and cancellation through the migrated commands.
func TestSearchOperatorReadFailureBoundaries(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	integration.ConfigureSearchMaintenance(t, testenv.OpenSearch(t))
	t.Setenv("OPENSEARCH_SHARDS", "1")
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", "24")
	t.Setenv("OPENSEARCH_REPLICAS", "0")
	fixture.Config.AuditReaderDSN = fixture.Config.DatabaseURL
	pool := ledgerAdminPool(t)
	var terminal []audit.Event

	for _, current := range []struct {
		command   string
		verb      audit.Verb
		reference string
	}{
		{command: "settings", verb: audit.VerbOpsSearchSettings, reference: "opensearch/settings"},
		{command: "verify", verb: audit.VerbOpsSearchVerify, reference: "opensearch/verification"},
		{command: "dlq", verb: audit.VerbOpsAuditDLQInspect, reference: "audit/dlq/summary"},
	} {
		arguments := []string{"ops", "search", current.command}
		if current.command == "dlq" {
			arguments = []string{"ops", "audit", "dlq", "inspect"}
		}

		t.Run(
			current.command+"_admission",
			func(t *testing.T) {
				operatorID := uuid.Must(uuid.NewV7())
				denied := rolePool(t, testenv.Ledger(t), lastChangeAppRole)
				cfg := fixture.Config
				var proxy *searchReadProxy
				if current.command == "settings" {
					cfg, proxy = newSearchReadProxy(t, cfg)
				}

				root, output := searchReadCommand(t, cfg, denied, operatorID, arguments...)
				err := root.ExecuteContext(t.Context())
				if !errors.Is(err, audit.ErrReadNotAdmitted) ||
					output.Len() != 0 {
					t.Fatalf(
						"admission refusal = %v, output = %q; want a refused admission before command output",
						err,
						output.String(),
					)
				}

				if events := readOutboxEvents(
					t,
					pool,
					current.verb,
					cli.ServiceActorID(searchReadService(operatorID)),
				); len(events) != 0 {
					t.Fatalf("refused admission recorded a legacy successful read: %+v", events)
				}

				if proxy != nil {
					if requests := proxy.observed(); len(requests) != 0 {
						t.Fatalf("refused admission executed real backend requests: %+v", requests)
					}

					control, printed := searchReadCommand(
						t,
						cfg,
						pool,
						uuid.Must(uuid.NewV7()),
						arguments...,
					)
					if err := control.ExecuteContext(t.Context()); err != nil {
						t.Fatalf(
							"the admitted settings control failed: %v\n%s",
							err,
							printed.String(),
						)
					}

					want := searchReadRequest{method: http.MethodGet, path: "/_cluster/settings"}
					if requests := proxy.observed(); !slices.Contains(requests, want) {
						t.Fatalf(
							"the admitted settings control did not execute %v: %+v",
							want,
							requests,
						)
					}

					t.Log(
						"real settings requests: refused admission=0; admitted control executed GET /_cluster/settings",
					)
				}
			},
		)
		t.Run(
			current.command+"_failure",
			func(t *testing.T) {
				cfg := *fixture.Config
				cfg.SearchEndpoint = ""
				if current.command == "dlq" {
					cfg.AuditReaderDSN = ""
				}

				operatorID := uuid.Must(uuid.NewV7())
				root, output := searchReadCommand(t, &cfg, pool, operatorID, arguments...)
				err := root.ExecuteContext(t.Context())
				if err == nil || output.Len() != 0 {
					t.Fatalf(
						"failed command = %v, output = %q; want the functional error without result bytes",
						err,
						output.String(),
					)
				}

				event := requireSearchReadEvent(
					t,
					pool,
					operatorID,
					current.verb,
					current.reference,
				)
				requireFailedSearchRead(t, event, err)
				terminal = append(terminal, event)
			},
		)
		t.Run(
			current.command+"_cancellation",
			func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				observed := cancellationReadPool(t, cancel)
				operatorID := uuid.Must(uuid.NewV7())
				root, output := searchReadCommand(
					t,
					fixture.Config,
					observed,
					operatorID,
					arguments...,
				)
				err := root.ExecuteContext(ctx)
				if !errors.Is(err, context.Canceled) || output.Len() != 0 {
					t.Fatalf(
						"canceled command = %v, output = %q; want context cancellation without result bytes",
						err,
						output.String(),
					)
				}

				event := requireSearchReadEvent(
					t,
					pool,
					operatorID,
					current.verb,
					current.reference,
				)
				requireFailedSearchRead(t, event, err)
				terminal = append(terminal, event)
			},
		)
	}

	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	pipeline.startRelay(t, nil)

	for _, event := range terminal {
		row := pipeline.stack().waitForRow(t, event.EventID)
		requireSearchReadLedgerIdentity(t, row, event)
		if row.Outcome != event.Outcome || row.Context.Read != nil || row.Error != nil {
			t.Fatalf(
				"ledger failure %s differs from the actual command result: %+v",
				event.EventID,
				row,
			)
		}

		t.Logf(
			"ledger event=%s operation=%s verb=%s outcome=%s",
			row.EventID,
			row.Context.OperationID,
			row.Action,
			row.Outcome,
		)
	}
}

func requireFailedSearchRead(t *testing.T, event audit.Event, commandError error) {
	t.Helper()
	if event.Outcome != audit.OutcomeAttempted || event.Error != nil ||
		event.Context.Read != nil {
		t.Fatalf("failed read = %+v, want the actual command error %v", event, commandError)
	}
}
