package integration_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/testenv"
)

func assertOperatorAuthentication(t *testing.T, dsn, password string, wantSuccess bool) {
	t.Helper()
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse the synthetic operator connection")
	}

	parsed.Password = password
	connection, err := pgx.ConnectConfig(t.Context(), parsed)
	if wantSuccess {
		if err != nil {
			t.Fatal("the expected password did not authenticate")
		}

		_ = connection.Close(t.Context())

		return
	}

	if err == nil {
		_ = connection.Close(t.Context())
		t.Fatal("the rejected password authenticated")
	}

	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "28P01" {
		t.Fatal("the rejected password failed without SQLSTATE 28P01")
	}
}

func assertSeedRolesOutcome(t *testing.T, events []audit.Event, outcome audit.Outcome) {
	t.Helper()
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 || len(events) == 0 || events[0].EventID != intents[0].EventID {
		t.Fatal("the operation did not record its intent first")
	}

	results := eventsInPhase(events, audit.PhaseResult, uuid.Nil)
	if len(results) != 1 || results[0].Outcome != outcome ||
		results[0].Context.OperationID != intents[0].Context.OperationID {
		t.Fatalf("the operation did not record the expected %s result", outcome)
	}
}

func verifySeedRolesPermanentLedger(
	t *testing.T,
	dsn string,
	admin *pgxpool.Pool,
	events []audit.Event,
	secrets []string,
) {
	t.Helper()
	brokers := testenv.Kafka(t)
	topic := "audit.seed-roles-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(
		t.Context(),
		audit.ConsumerConfig{
			Brokers:      []string{brokers},
			Topic:        topic,
			GroupID:      "seed-roles-" + uuid.NewString()[:8],
			BatchSize:    32,
			PollInterval: 100 * time.Millisecond,
			YugabyteDSN:  dsn,
		},
	)
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}

	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	reader, err := audit.NewReader(t.Context(), dsn)
	if err != nil {
		t.Fatal("open the disposable audit ledger")
	}

	t.Cleanup(reader.Close)
	opened, err := auditrelay.Open(
		t.Context(),
		auditrelay.Config{
			Brokers:               []string{brokers},
			Topic:                 topic,
			YugabyteDSN:           dsn,
			FDBClusterFile:        testenv.FoundationDB(t),
			FDBTransactionTimeout: 5 * time.Second,
			PollInterval:          100 * time.Millisecond,
			BatchSize:             64,
		},
	)
	if err != nil {
		t.Fatal("open the audit relay")
	}

	opened.Relay.Start(t.Context())
	t.Cleanup(func() { _ = opened.Relay.Close(); opened.Pool.Close() })
	stack := runtimeAuditStack{topic: topic, admin: admin, reader: reader, recorder: nil}

	for _, event := range events {
		row := stack.waitForRow(t, event.EventID)
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("encode the permanent audit row: %v", err)
		}

		assertRoleSecretsAbsent(t, encoded, secrets...)
		if row.ActorID != event.Actor.ID || row.Action != event.Verb ||
			row.Context.Phase != event.Context.Phase ||
			row.Outcome != event.Outcome ||
			row.Context.OperationID != event.Context.OperationID {
			t.Fatalf(
				"permanent audit event %s does not match its recorded actor and outcome",
				event.EventID,
			)
		}
	}
}
