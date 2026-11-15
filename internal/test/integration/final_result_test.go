package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// requirePermanentSearchResults delivers the command and final-result events
// through the real relay and consumer, then verifies their original identities.
func requirePermanentSearchResults(
	t *testing.T,
	initiations ...searchdomain.Initiation,
) runtimeAuditStack {
	t.Helper()
	stack := startRuntimeAuditStack(t)
	opened, err := auditrelay.Open(
		t.Context(),
		auditrelay.Config{
			Brokers:               []string{testenv.Kafka(t)},
			Topic:                 stack.topic,
			YugabyteDSN:           testenv.Ledger(t),
			FDBClusterFile:        testenv.FoundationDB(t),
			FDBTransactionTimeout: integration.StoreTransactionTimeout,
			PollInterval:          100 * time.Millisecond,
			BatchSize:             64,
		},
	)
	if err != nil {
		t.Fatalf("open command-result audit relay: %v", err)
	}

	opened.Relay.Start(t.Context())
	t.Cleanup(opened.Pool.Close)
	t.Cleanup(func() {
		if err := opened.Relay.Close(); err != nil {
			t.Errorf("close command-result audit relay: %v", err)
		}
	})

	for _, initiation := range initiations {
		requirePermanentSearchResult(t, stack, initiation, audit.OutcomeCompleted)
	}

	return stack
}

func requirePermanentSearchResult(
	t *testing.T,
	stack runtimeAuditStack,
	initiation searchdomain.Initiation,
	expectedOutcome audit.Outcome,
) {
	t.Helper()
	var original audit.Event
	if err := json.Unmarshal(initiation.Operation, &original); err != nil {
		t.Fatalf("decode initiating command %s: %v", initiation.OperationID, err)
	}

	intent := stack.waitForRow(t, original.EventID)
	resultID := uuid.NewSHA1(initiation.OperationID, []byte("search-operation-result"))
	result := stack.waitForRow(t, resultID)
	if result.ActorID != initiation.ActorID || result.ActorKind != intent.ActorKind ||
		result.Action != original.Verb || result.Outcome != expectedOutcome ||
		result.Context.OperationID != initiation.OperationID ||
		result.Context.ParentOperationID != original.Context.ParentOperationID ||
		result.Context.Phase != audit.PhaseResult ||
		result.Context.RequestID != original.Context.RequestID ||
		result.Context.TraceID != original.Context.TraceID ||
		!result.Context.StartedAt.Equal(original.Context.StartedAt) ||
		result.Context.CompletedAt.Before(original.Context.StartedAt) ||
		result.Context.Target == nil || original.Context.Target == nil ||
		*result.Context.Target != *original.Context.Target {
		t.Fatalf("final search result changed initiating identity or outcome: %+v", result)
	}

	if expectedOutcome == audit.OutcomePartiallyCompleted &&
		len(result.Context.CommittedSteps) != 1 {
		t.Fatalf("rolled-back rollout lost its committed activation: %+v", result.Context)
	}

	var extra struct {
		Result struct {
			RebuildID     uuid.UUID `json:"rebuild_id"`
			SourceIndex   string    `json:"source_index"`
			TargetIndex   string    `json:"target_index"`
			State         string    `json:"state"`
			AuthorityID   uuid.UUID `json:"authority_id"`
			ActiveVersion string    `json:"active_version"`
			Generation    int64     `json:"generation"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result.Extra, &extra); err != nil {
		t.Fatalf("final search result lacks its observed result: %s (%v)", result.Extra, err)
	}

	if original.Verb == string(audit.VerbOpsSearchReindex) &&
		(extra.Result.RebuildID == uuid.Nil || extra.Result.SourceIndex == "" ||
			extra.Result.TargetIndex == "" || extra.Result.State != string(
			audit.OutcomeCompleted,
		)) {
		t.Fatalf("final reindex result lacks its completed replacement: %s", result.Extra)
	}

	if (original.Verb == string(
		audit.VerbOpsSearchAccessRollout,
	) || original.Verb == string(audit.VerbOpsSearchAccessRollback)) &&
		(extra.Result.AuthorityID == uuid.Nil || extra.Result.ActiveVersion == "" || extra.Result.Generation < 1 ||
			extra.Result.State != string(expectedOutcome)) {
		t.Fatalf("final rollout result lacks its serving version: %s", result.Extra)
	}

	if err := stack.exportAndVerify(
		t,
		result.OrgID,
		original.OccurredAt.Add(-time.Second),
		result.EventTime.Add(time.Second),
	); err != nil {
		t.Fatalf("verify command-result export for %s: %v", initiation.OperationID, err)
	}
}
