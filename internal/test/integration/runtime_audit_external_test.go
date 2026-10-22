package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestRuntimeAuditExternalRecovery interrupts an operation after an S3 upload
// and before recording the step result. It retries the same intent and checks
// that the retry does not upload the object again. After the relay publishes
// the outbox events and the consumer records them in the ledger, the test
// checks each event's outcome and phase.
func TestRuntimeAuditExternalRecovery(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	outbox := audit.NewPoolOutbox(stack.admin)
	bucket := testenv.ObjectStore(t)
	client := objectStoreClient(bucket)
	key := "runtime-audit/" + uuid.NewString()
	objectTarget := externalReference("s3/" + bucket.Bucket + "/" + key)
	intent := externalIntent(externalReference("s3/" + bucket.Bucket))

	attemptContext, interrupt := context.WithCancel(t.Context())
	err := audit.RunExternal(attemptContext, outbox, intent, func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
		if err := steps.BeginStep(ctx, audit.ExternalStep(objectTarget, nil)); err != nil {
			return audit.OutcomeDetails{}, err
		}
		putRecoveryObject(ctx, t, client, bucket.Bucket, key)
		interrupt()
		return audit.OutcomeDetails{}, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the interrupted attempt returned %v instead of context.Canceled", err)
	}
	firstTag := recoveryObjectTag(t, client, bucket.Bucket, key)

	repeated := false
	err = audit.RunExternal(t.Context(), outbox, intent, func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
		step := audit.ExternalStep(objectTarget, nil)
		if err := steps.BeginStep(ctx, step); err != nil {
			return audit.OutcomeDetails{}, err
		}
		if _, headErr := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket.Bucket), Key: aws.String(key)}); headErr != nil {
			putRecoveryObject(ctx, t, client, bucket.Bucket, key)
			repeated = true
		}
		completed := audit.OutcomeDetails{Outcome: audit.OutcomeCompleted, FailureCode: "", FailureMessage: "", CommittedSteps: nil}
		return completed, steps.FinishStep(ctx, audit.FinishedStep(step, completed, json.RawMessage(`{"state":"already_present"}`)))
	})
	if err != nil {
		t.Fatalf("the second attempt with the same intent failed: %v", err)
	}
	if repeated || recoveryObjectTag(t, client, bucket.Bucket, key) != firstTag {
		t.Fatal("the second attempt repeated the upload or changed the object's ETag")
	}

	events := outboxEventsByQuery(t, stack.admin, "runtime_audit_operation_outbox.sql", intent.Context.OperationID.String())
	requireRecoveryEvents(t, intent, objectTarget, events)
	startOutboxRelay(t, stack)
	for _, event := range events {
		row := stack.waitForRow(t, event.EventID)
		if row.Outcome != event.Outcome || row.Context.Phase != event.Context.Phase {
			t.Fatalf("ledger event %s has outcome %s and phase %s instead of %s and %s",
				event.EventID, row.Outcome, row.Context.Phase, event.Outcome, event.Context.Phase)
		}
	}
}

func requireRecoveryEvents(t *testing.T, intent audit.Event, objectTarget audit.Target, events []audit.Event) {
	t.Helper()
	operationID := intent.Context.OperationID
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	stepIntents := eventsInPhase(events, audit.PhaseIntent, operationID)
	results := eventsInPhase(events, audit.PhaseResult, uuid.Nil)
	stepResults := eventsInPhase(events, audit.PhaseResult, operationID)
	if len(events) != 5 || len(intents) != 1 || len(stepIntents) != 1 || len(results) != 2 || len(stepResults) != 1 {
		t.Fatalf("the outbox contains %d events: %d intents, %d step intents, %d results, %d step results instead of 5 (1, 1, 2, 1)",
			len(events), len(intents), len(stepIntents), len(results), len(stepResults))
	}
	if intents[0].EventID != intent.EventID {
		t.Fatalf("the outbox intent has ID %s instead of %s", intents[0].EventID, intent.EventID)
	}
	if stepIntents[0].EventID != audit.StepIntentEventID(operationID, objectTarget) {
		t.Fatalf("step intent has ID %s instead of %s", stepIntents[0].EventID, audit.StepIntentEventID(operationID, objectTarget))
	}
	if stepResults[0].Outcome != audit.OutcomeCompleted || stepResults[0].Context.OperationID != audit.StepOperationID(operationID, objectTarget) {
		t.Fatalf("step result has unexpected outcome %s or operation %s", stepResults[0].Outcome, stepResults[0].Context.OperationID)
	}
	first, second := results[0], results[1]
	if first.Outcome != audit.OutcomeIncomplete || first.Error == nil || first.Error.Code != "step_unfinished" ||
		!strings.Contains(first.Error.Message, objectTarget.Reference) {
		t.Fatalf("result %s %+v must be incomplete with step_unfinished for %s", first.Outcome, first.Error, objectTarget.Reference)
	}
	if second.Outcome != audit.OutcomeCompleted || second.Error != nil {
		t.Fatalf("the second result has outcome %s and error %+v instead of completed without an error", second.Outcome, second.Error)
	}
}

func putRecoveryObject(ctx context.Context, t *testing.T, client *s3.Client, bucket, key string) {
	t.Helper()
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader("runtime audit recovery object " + uuid.NewString()),
	})
	if err != nil {
		t.Fatalf("the upload of object %s failed: %v", key, err)
	}
}

func recoveryObjectTag(t *testing.T, client *s3.Client, bucket, key string) string {
	t.Helper()
	head, err := client.HeadObject(t.Context(), &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("the metadata read for object %s failed: %v", key, err)
	}
	return aws.ToString(head.ETag)
}
