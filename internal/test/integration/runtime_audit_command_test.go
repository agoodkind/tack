package integration_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

type bucketStepEvidence struct {
	Bucket string `json:"bucket"`
	State  string `json:"state"`
}

type commandExtra struct {
	OpID       uuid.UUID `json:"op_id"`
	SessionID  string    `json:"session_id"`
	OnBehalfOf *struct {
		OperatorID uuid.UUID `json:"operator_id"`
	} `json:"on_behalf_of"`
}

// backupBucketConfig returns a backup configuration with the object store's
// writer credentials and a new bucket name.
func backupBucketConfig(bucket testenv.ObjectStoreBucket) *config.Config {
	return &config.Config{
		BackupS3Endpoint: bucket.Endpoint, BackupS3Region: bucket.Region,
		BackupS3BucketMain: "rt-audit-" + strings.ReplaceAll(uuid.NewString()[:13], "-", ""),
		BackupS3AccessKey:  bucket.AccessKey, BackupS3SecretKey: bucket.SecretKey,
	}
}

// TestRuntimeAuditBackgroundIdentity checks that recurring service commands
// record a stable service actor ID and distinct operation IDs. The second
// buckets-init run also records the accountable operator and agent session.
// The test checks that the first run creates the bucket and the second
// records that the bucket already exists.
func TestRuntimeAuditBackgroundIdentity(t *testing.T) {
	pool := ledgerAdminPool(t)
	cfg := backupBucketConfig(testenv.ObjectStore(t))
	service := "tack-backup-timer-" + uuid.NewString()[:8]
	operatorID := uuid.Must(uuid.NewV7())
	session := "session-" + uuid.NewString()[:8]
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-service", service, "ops", "backup", "buckets-init"); err != nil {
		t.Fatalf("the first buckets-init run failed: %v\n%s", err, output)
	}
	if output, err := runOpsCommand(t, cfg, pool, "--execute", "--operator-service", service, "--operator-session", session,
		"--operator-id", operatorID.String(), "--operator-email", "timer-owner@example.invalid",
		"ops", "backup", "buckets-init"); err != nil {
		t.Fatalf("the second buckets-init run failed: %v\n%s", err, output)
	}

	serviceID := cli.ServiceActorID(service)
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsBackupBucketsInit), serviceID.String())
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 2 || intents[0].Context.OperationID == intents[1].Context.OperationID {
		t.Fatalf("the test expects two intents with distinct operation IDs but found %d with %v", len(intents), operationIDs(intents))
	}
	wantStates := []string{"created", "already_present"}
	for index, intent := range intents {
		if intent.Actor.Type != audit.ActorService || intent.Actor.ID != serviceID {
			t.Fatalf("intent %s records actor %s %s instead of service %s", intent.EventID, intent.Actor.Type, intent.Actor.ID, serviceID)
		}
		extra := decodeCommandExtra(t, intent)
		if extra.OpID != intent.Context.OperationID {
			t.Fatalf("intent %s records op_id %s instead of %s", intent.EventID, extra.OpID, intent.Context.OperationID)
		}
		requireBucketStep(t, events, intent.Context.OperationID, cfg.BackupS3BucketMain, wantStates[index])
	}
	if first := decodeCommandExtra(t, intents[0]); first.OnBehalfOf != nil || first.SessionID != "" {
		t.Fatalf("the first run records extra %+v but must not record an accountable operator or session", first)
	}
	second := decodeCommandExtra(t, intents[1])
	if second.OnBehalfOf == nil || second.OnBehalfOf.OperatorID != operatorID || second.SessionID != session {
		t.Fatalf("the second run records extra %+v instead of operator %s and session %s", second, operatorID, session)
	}
}

func requireBucketStep(t *testing.T, events []audit.Event, operationID uuid.UUID, bucket, state string) {
	t.Helper()
	stepResults := eventsInPhase(events, audit.PhaseResult, operationID)
	if len(stepResults) != 1 || stepResults[0].Outcome != audit.OutcomeCompleted {
		t.Fatalf("operation %s has %d step results; the test expects one completed step result", operationID, len(stepResults))
	}
	var evidence bucketStepEvidence
	if err := json.Unmarshal(stepResults[0].Extra, &evidence); err != nil {
		t.Fatalf("decoding step evidence for operation %s failed: %v", operationID, err)
	}
	if evidence.Bucket != bucket || evidence.State != state {
		t.Fatalf("operation %s records step evidence %+v instead of bucket %s and state %s", operationID, evidence, bucket, state)
	}
	for _, result := range eventsInPhase(events, audit.PhaseResult, uuid.Nil) {
		if result.Context.OperationID == operationID && result.Outcome == audit.OutcomeCompleted {
			return
		}
	}
	t.Fatalf("operation %s does not have a completed result", operationID)
}

func decodeCommandExtra(t *testing.T, event audit.Event) commandExtra {
	t.Helper()
	var extra commandExtra
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decoding Extra for event %s failed: %v", event.EventID, err)
	}
	return extra
}

func operationIDs(events []audit.Event) []uuid.UUID {
	identifiers := make([]uuid.UUID, 0, len(events))
	for _, event := range events {
		identifiers = append(identifiers, event.Context.OperationID)
	}
	return identifiers
}

// ledgerAdminPool opens an administrator pool on the test ledger.
func ledgerAdminPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("opening the test ledger pool failed: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
