package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/testenv"
)

type rolloutStepEvidence struct {
	AuthorityID      string `json:"authority_id"`
	CandidateVersion string `json:"candidate_version"`
	Phase            string `json:"phase"`
	Generation       int64  `json:"generation"`
}

func testAccessRolloutOutcomes(t *testing.T) {
	clusterFile := testenv.FoundationDB(t)
	prefix := append([]byte("tack-test:"), []byte(uuid.NewString())...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
	pool := ledgerAdminPool(t)
	cfg := &config.Config{DatabaseURL: testenv.Ledger(t), FDBClusterFile: clusterFile, FDBTransactionTimeout: 5 * time.Second}
	authority := uuid.Must(uuid.NewV7())
	command := []string{"ops", "search", "access-rollout", "--authority", authority.String(), "--candidate", searchaccess.RotatedVersion}
	requireSearchPreviewRead(t, cfg, pool, audit.VerbOpsSearchAccessRollout, command...)
	operatorID := uuid.Must(uuid.NewV7())
	arguments := append([]string{"--execute", "--operator-id", operatorID.String(), "--operator-email", outcomeOperatorEmail}, command...)
	for range 2 {
		if output, err := runOpsCommand(t, cfg, pool, arguments...); err != nil {
			t.Fatalf("access-rollout failed: %v\n%s", err, output)
		}
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsSearchAccessRollout), operatorID.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 2 {
		t.Fatalf("access-rollout recorded %d intents instead of 2", len(intents))
	}
	first, second := intents[0].Context.OperationID, intents[1].Context.OperationID
	stepResults := eventsInPhase(events, audit.PhaseResult, first)
	if len(stepResults) != 1 {
		t.Fatalf("the first access-rollout recorded %d step results instead of 1", len(stepResults))
	}
	var evidence rolloutStepEvidence
	if err := json.Unmarshal(stepResults[0].Extra, &evidence); err != nil {
		t.Fatalf("decoding the rollout evidence failed: %v", err)
	}
	if evidence.AuthorityID != authority.String() || evidence.CandidateVersion != searchaccess.RotatedVersion ||
		evidence.Phase != "backfill" || evidence.Generation == 0 {
		t.Fatalf("access-rollout recorded %+v; the test requires authority %s, candidate %s, phase backfill, and a nonzero generation",
			evidence, authority, searchaccess.RotatedVersion)
	}
	requireCompletedResult(t, eventsOfOperation(events, first), first)
	requireInitiation(t, "access rollout", storedRolloutInitiation(t, clusterFile, authority), first, operatorID)
	if steps := eventsInPhase(events, audit.PhaseIntent, second); len(steps) != 0 {
		t.Fatalf("the second access-rollout recorded %d step intents instead of 0", len(steps))
	}
	requireOnlyResult(t, eventsOfOperation(events, second), audit.OutcomeRefused, "rollout_in_progress")
	requireRollbackInitiation(t, cfg, pool, authority)
	requireUnknownCandidateRefused(t, cfg, pool)
}

// requireRollbackInitiation rolls back the access rollout of authority as a
// new operator and requires the rollout record to store the operation ID and
// operator of the rollback command.
func requireRollbackInitiation(t *testing.T, cfg *config.Config, pool *pgxpool.Pool, authority uuid.UUID) {
	t.Helper()
	operatorID := uuid.Must(uuid.NewV7())
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-id", operatorID.String(), "--operator-email", outcomeOperatorEmail,
		"ops", "search", "access-rollback", "--authority", authority.String()); err != nil {
		t.Fatalf("access-rollback failed: %v\n%s", err, output)
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsSearchAccessRollback), operatorID.String())
	if len(events) == 0 {
		t.Fatal("access-rollback did not record an event")
	}
	operationID := decodeCommandExtra(t, events[0]).OpID
	requireInitiation(t, "rolled back access rollout", storedRolloutInitiation(t, cfg.FDBClusterFile, authority), operationID, operatorID)
}

func requireUnknownCandidateRefused(t *testing.T, cfg *config.Config, pool *pgxpool.Pool) {
	t.Helper()
	operatorID := uuid.Must(uuid.NewV7())
	output, err := runOpsCommand(t, cfg, pool,
		"--execute", "--operator-id", operatorID.String(), "--operator-email", outcomeOperatorEmail,
		"ops", "search", "access-rollout", "--authority", uuid.Must(uuid.NewV7()).String(), "--candidate", "unregistered-version")
	if err == nil {
		t.Fatalf("access-rollout succeeded with an unregistered candidate\n%s", output)
	}
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsSearchAccessRollout), operatorID.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 1 {
		t.Fatalf("access-rollout recorded %d intents for an unregistered candidate instead of 1", len(intents))
	}
	operation := intents[0].Context.OperationID
	if steps := eventsInPhase(events, audit.PhaseIntent, operation); len(steps) != 0 {
		t.Fatalf("access-rollout recorded %d step intents for an unregistered candidate instead of 0", len(steps))
	}
	requireOnlyResult(t, eventsOfOperation(events, operation), audit.OutcomeRefused, "input_invalid")
}
