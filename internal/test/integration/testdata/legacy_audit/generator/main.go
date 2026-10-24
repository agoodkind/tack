// Command legacyfixture prints audit records that this revision's own writers
// produce, as JSON, for the runtime audit legacy writer tests.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

// fixture is one record in the order the writer produced it. PartitionBytes
// contains the Kafka record key in hexadecimal. Value contains the record
// bytes as text, because every writer encodes the value as JSON.
type fixture struct {
	Kind           string    `json:"kind"`
	EventID        uuid.UUID `json:"event_id"`
	OrgID          uuid.UUID `json:"org_id"`
	Verb           string    `json:"verb"`
	PartitionBytes string    `json:"partition_bytes,omitempty"`
	Value          string    `json:"value"`
}

type captureRecorder struct{ events []audit.Event }

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) error {
	c.events = append(c.events, ev)
	return nil
}

type refusingRecorder struct{}

func (refusingRecorder) Record(context.Context, audit.Event) error {
	return errors.New("broker refused the record")
}

type captureAppender struct{ payloads []json.RawMessage }

func (c *captureAppender) Append(_ context.Context, event json.RawMessage) error {
	c.payloads = append(c.payloads, append(json.RawMessage(nil), event...))
	return nil
}

type captureOutbox struct{ events []audit.Event }

func (c *captureOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	c.events = append(c.events, event)
	return nil
}

type staticSource struct{ principal audit.OperatorPrincipal }

func (s staticSource) Resolve(context.Context) (audit.OperatorPrincipal, error) {
	return s.principal, nil
}

func main() {
	fixtures, err := generate(context.Background(), os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(fixtures); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(ctx context.Context, deployCommit string) ([]fixture, error) {
	orgID := uuid.Must(uuid.NewV7())
	userID := uuid.Must(uuid.NewV7())
	var out []fixture

	// The MCP wrapper and auth middleware record through CanonicalRecorder,
	// and KafkaRecorder.Record produces MarshalEvent bytes under the
	// partition key.
	kafka := &captureRecorder{}
	recorder := audit.CanonicalRecorder{Inner: kafka}
	tool := audit.Event{
		Verb: string(audit.VerbMCPToolInvoked), Actor: audit.Actor{Type: audit.ActorUser, ID: userID, RequestID: "req-legacy-1"},
		Entity:  audit.Entity{Type: "mcp_tool", Name: "tack_update_issue"},
		Context: audit.EventContext{OrgID: orgID, Source: audit.SourceMCP, Tool: "tack_update_issue", RequestID: "req-legacy-1"},
		Outcome: audit.OutcomeOK,
	}
	update := tool
	update.Verb = string(audit.VerbNodeUpdate)
	update.Entity = audit.Entity{Type: "node", NodeType: "issue", ID: uuid.Must(uuid.NewV7()), Identifier: "LEG-1", Name: "Legacy issue"}
	update.Delta = &audit.Delta{Before: json.RawMessage(`{"name":"Old"}`), After: json.RawMessage(`{"name":"Legacy issue"}`), Changed: []string{"name"}}
	used := audit.Event{
		Verb:    string(audit.VerbAuthTokenUsed),
		Actor:   audit.Actor{Type: audit.ActorUser, ID: userID, APITokenLabel: "sha256:legacy"},
		Entity:  audit.Entity{Type: "auth", ID: userID, Name: "token_accepted"},
		Outcome: audit.OutcomeOK,
	}
	used.Context.OrgID = orgID
	used.Context.APITokenID = uuid.Must(uuid.NewV7())
	dead := tool
	dead.Context.RequestID = "req-legacy-dead-letter"
	for _, ev := range []audit.Event{tool, update, used, dead} {
		if err := recorder.Record(ctx, ev); err != nil {
			return nil, err
		}
	}
	for index, ev := range kafka.events {
		value, err := audit.MarshalEvent(ev)
		if err != nil {
			return nil, err
		}
		kind := "kafka"
		if index == len(kafka.events)-1 {
			kind = "dlq"
		}
		out = append(out, fixture{Kind: kind, EventID: ev.EventID, OrgID: ev.Context.OrgID, Verb: ev.Verb, PartitionBytes: hex.EncodeToString(audit.LegacyFixturePartitionKey(ev)), Value: string(value)})
	}

	// An operator command writes its intent and outcome through the
	// clispec choke-point into the operator outbox.
	outbox := &captureOutbox{}
	source := staticSource{principal: audit.OperatorPrincipal{
		ID: uuid.Must(uuid.NewV7()), Email: "operator@example.invalid", Name: "Legacy Operator", Source: "flags",
	}}
	mutate := audit.Spec{Verb: string(audit.VerbOpsAuthTokenCreate), Mutates: true}
	if err := clispec.RunAudited(ctx, io.Discard, mutate, source, deployCommit, true, outbox, nil,
		func(context.Context) error { return nil }); err != nil {
		return nil, err
	}
	read := audit.Spec{Verb: string(audit.VerbOpsDeployVerify), Reads: true}
	if err := clispec.RunAudited(ctx, io.Discard, read, source, deployCommit, true, outbox, nil,
		func(context.Context) error { return nil }); err != nil {
		return nil, err
	}
	for _, ev := range outbox.events {
		value, err := audit.LegacyFixtureOutboxJSON(ev)
		if err != nil {
			return nil, err
		}
		out = append(out, fixture{Kind: "ops_outbox", EventID: ev.EventID, OrgID: ev.Context.OrgID, Verb: ev.Verb, Value: string(value)})
	}

	// A refused broker record spills to the FoundationDB operator outbox.
	spill := &captureAppender{}
	spilling := audit.CanonicalRecorder{Inner: &audit.SpillRecorder{Primary: refusingRecorder{}, Spill: spill}}
	spilled := tool
	spilled.Context.RequestID = "req-legacy-spill"
	if err := spilling.Record(ctx, spilled); err != nil {
		return nil, err
	}
	staged, err := stagedIntents(ctx, orgID, userID)
	if err != nil {
		return nil, err
	}
	for _, payload := range append(spill.payloads, staged...) {
		var ev audit.Event
		if err := json.Unmarshal(payload, &ev); err != nil {
			return nil, err
		}
		out = append(out, fixture{Kind: "fdb_outbox", EventID: ev.EventID, OrgID: ev.Context.OrgID, Verb: ev.Verb, Value: string(payload)})
	}
	return out, nil
}
