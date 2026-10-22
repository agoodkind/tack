package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

const (
	reindexServingIndex  = "node-pages-1"
	rolesLedgerContainer = "tack-test-roles-"
)

type replacementStepEvidence struct {
	SourceIndex string `json:"source_index"`
	RebuildID   string `json:"rebuild_id"`
	Generation  int64  `json:"generation"`
}

type roleStepEvidence struct {
	Role   string `json:"role"`
	Before struct {
		Exists bool `json:"exists"`
	} `json:"before"`
	After *struct {
		Exists   bool     `json:"exists"`
		Login    bool     `json:"login"`
		MemberOf []string `json:"member_of"`
	} `json:"after"`
	PasswordSet bool `json:"password_set"`
}

// testSearchReindexOutcomes runs ops search reindex twice against a serving
// index in FoundationDB. The first run records the replacement it began.
// The command refuses the second run because that replacement is in progress.
func testSearchReindexOutcomes(t *testing.T) {
	clusterFile := testenv.FoundationDB(t)
	prefix := append([]byte("tack-test:"), []byte(uuid.NewString())...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
	stores, err := fdbadapter.NewStores(clusterFile, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("opening FoundationDB stores failed: %v", err)
	}
	if err := stores.InitializeSearchIndex(t.Context(), reindexServingIndex); err != nil {
		t.Fatalf("recording the serving search index failed: %v", err)
	}
	t.Setenv("OPENSEARCH_SHARDS", "1")
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", "24")
	t.Setenv("OPENSEARCH_REPLICAS", "0")
	pool := ledgerAdminPool(t)
	cfg := &config.Config{DatabaseURL: testenv.Ledger(t), FDBClusterFile: clusterFile, FDBTransactionTimeout: 5 * time.Second}
	operatorID := uuid.Must(uuid.NewV7())
	for range 2 {
		if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
			"--operator-email", outcomeOperatorEmail, "ops", "search", "reindex"); err != nil {
			t.Fatalf("search reindex failed: %v\n%s", err, output)
		}
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsSearchReindex), operatorID.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 2 {
		t.Fatalf("reindex recorded %d intents instead of 2", len(intents))
	}
	first, second := intents[0].Context.OperationID, intents[1].Context.OperationID
	stepResults := eventsInPhase(events, audit.PhaseResult, first)
	if len(stepResults) != 1 {
		t.Fatalf("the first reindex recorded %d step results instead of 1", len(stepResults))
	}
	var evidence replacementStepEvidence
	if err := json.Unmarshal(stepResults[0].Extra, &evidence); err != nil {
		t.Fatalf("decoding the replacement evidence failed: %v", err)
	}
	if evidence.SourceIndex != reindexServingIndex || evidence.RebuildID == "" || evidence.RebuildID == uuid.Nil.String() {
		t.Fatalf("replacement evidence %+v must include source %s and a replacement ID", evidence, reindexServingIndex)
	}
	requireCompletedResult(t, eventsOfOperation(events, first), first)
	if steps := eventsInPhase(events, audit.PhaseIntent, second); len(steps) != 0 {
		t.Fatalf("the second reindex recorded %d step intents instead of 0", len(steps))
	}
	requireOnlyResult(t, eventsOfOperation(events, second), audit.OutcomeRefused, "replacement_in_progress")
}

// testSeedRolesOutcomes runs ops audit seed-roles on an empty migrated ledger
// node. Each login role step records the role before and after the seed.
// The events do not contain role passwords.
func testSeedRolesOutcomes(t *testing.T) {
	node := testenv.StartEmptyLedger(t, rolesLedgerContainer+uuid.NewString()[:8])
	if err := postgres.Migrate(t.Context(), node.DSN, migrations.FS); err != nil {
		t.Fatalf("migrating the empty ledger failed: %v", err)
	}
	pool, err := pgxpool.New(t.Context(), node.DSN)
	if err != nil {
		t.Fatalf("opening the ledger pool failed: %v", err)
	}
	t.Cleanup(pool.Close)
	cfg := &config.Config{DatabaseURL: node.DSN}
	passwords := []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword, &cfg.MigratorPassword,
	}
	for _, password := range passwords {
		*password = "role-secret-" + uuid.NewString()
	}
	operatorID := uuid.Must(uuid.NewV7())
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", outcomeOperatorEmail, "ops", "audit", "seed-roles"); err != nil {
		t.Fatalf("seed-roles failed: %v\n%s", err, output)
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbAuditRolesSeed), operatorID.String())
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encoding event %s failed: %v", event.EventID, err)
		}
		for _, password := range passwords {
			if strings.Contains(string(encoded), *password) {
				t.Fatalf("event %s contains a role password", event.EventID)
			}
		}
	}
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("seed-roles recorded %d intents instead of 1", len(intents))
	}
	requireRoleSteps(t, eventsInPhase(events, audit.PhaseResult, intents[0].Context.OperationID))
	requireCompletedResult(t, events, intents[0].Context.OperationID)
}
