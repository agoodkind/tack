package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

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
	operatorID := uuid.Must(uuid.NewV7())
	arguments := []string{
		"--execute", "--operator-id", operatorID.String(), "--operator-email", outcomeOperatorEmail,
		"ops", "search", "access-rollout", "--authority", authority.String(), "--candidate", searchaccess.RotatedVersion,
	}
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
	if steps := eventsInPhase(events, audit.PhaseIntent, second); len(steps) != 0 {
		t.Fatalf("the second access-rollout recorded %d step intents instead of 0", len(steps))
	}
	requireOnlyResult(t, eventsOfOperation(events, second), audit.OutcomeRefused, "rollout_in_progress")
}
