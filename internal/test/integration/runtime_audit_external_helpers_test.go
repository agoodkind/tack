package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

func operatorActor() audit.Actor {
	return audit.Actor{
		Type: audit.ActorOperator, ID: uuid.Must(uuid.NewV7()), Email: "", Name: "",
		SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
}

func externalReference(reference string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Reference: reference, Name: "", URI: "",
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// externalIntent returns a format 2 external intent event for target with a
// new operation ID. The event does not specify a parent operation.
func externalIntent(target audit.Target) audit.Event {
	intent := completeEvent(audit.SystemOrgID(), operatorActor(), target, audit.OperationExternal, audit.PhaseIntent, audit.OutcomePending)
	intent.Context.Source = audit.SourceSystem
	intent.Context.ParentOperationID = uuid.Nil
	intent.Context.CompletedAt = time.Time{}
	return intent
}

func outboxEventsByQuery(t *testing.T, pool *pgxpool.Pool, query string, arguments ...string) []audit.Event {
	t.Helper()
	values := make([]any, 0, len(arguments))
	for _, argument := range arguments {
		values = append(values, argument)
	}
	rows, err := pool.Query(t.Context(), readTestSQL(t, query), values...)
	if err != nil {
		t.Fatalf("the test could not read the outbox with %s: %v", query, err)
	}
	encoded, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatalf("the test could not scan the outbox rows from %s: %v", query, err)
	}
	events := make([]audit.Event, 0, len(encoded))
	for _, body := range encoded {
		var event audit.Event
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatalf("the test could not decode an outbox event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func eventsInPhase(events []audit.Event, phase audit.Phase, parent uuid.UUID) []audit.Event {
	var selected []audit.Event
	for _, event := range events {
		if event.Context.Phase == phase && event.Context.ParentOperationID == parent {
			selected = append(selected, event)
		}
	}
	return selected
}

// objectStoreClient returns an S3 client for the writer identity of bucket.
func objectStoreClient(bucket testenv.ObjectStoreBucket) *s3.Client {
	settings := aws.Config{
		Region:      bucket.Region,
		Credentials: credentials.NewStaticCredentialsProvider(bucket.AccessKey, bucket.SecretKey, ""),
	}
	return s3.NewFromConfig(settings, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(bucket.Endpoint)
		options.UsePathStyle = true
	})
}

func runOpsCommand(t *testing.T, cfg *config.Config, pool *pgxpool.Pool, args ...string) (string, error) {
	t.Helper()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	factory.Err = &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
	registry := clispec.NewRegistry()
	ops.RegisterCommands(registry, factory)
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, factory) {
		root.AddCommand(command)
	}
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return output.String(), err
}
