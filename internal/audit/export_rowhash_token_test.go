package audit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// contextBeforeTokenID is EventContext as the writer marshalled it before
// f48a5a6 (TACK-502) added the token id.
type contextBeforeTokenID struct {
	OrgID       uuid.UUID `json:"org_id"`
	WorkspaceID uuid.UUID `json:"workspace_id,omitempty"`
	ScopeID     uuid.UUID `json:"scope_id,omitempty"`
	ParentID    uuid.UUID `json:"parent_id,omitempty"`
	RequestID   string    `json:"request_id,omitempty"`
	TraceID     string    `json:"trace_id,omitempty"`
	Source      Source    `json:"source"`
	Tool        string    `json:"tool,omitempty"`
	RPC         string    `json:"rpc,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// contextWithOmitemptyTokenID is EventContext as the writer marshalled it from
// f48a5a6 until TACK-514: omitempty on an array never omits, so a zero token
// id was written as an explicit zero UUID.
type contextWithOmitemptyTokenID struct {
	contextBeforeTokenID

	APITokenID uuid.UUID `json:"api_token_id,omitempty"`
}

func contextBeforeTokenIDFrom(context EventContext) contextBeforeTokenID {
	return contextBeforeTokenID{
		OrgID: context.OrgID, WorkspaceID: context.WorkspaceID, ScopeID: context.ScopeID,
		ParentID: context.ParentID, RequestID: context.RequestID, TraceID: context.TraceID,
		Source: context.Source, Tool: context.Tool, RPC: context.RPC, Reason: context.Reason,
	}
}

// chainTokenIDTestRows builds one chain of rows, each hashed from the context
// bytes its writer produced, and links every row to its predecessor.
func chainTokenIDTestRows(t *testing.T, rows []Row, contexts [][]byte) []Row {
	t.Helper()
	var prev []byte
	for i := range rows {
		rows[i].Seq = int64(i + 1)
		rows[i].PrevHash = prev
		rows[i].RowHash = hashTokenIDTestRow(t, rows[i], contexts[i])
		prev = rows[i].RowHash
	}
	return rows
}

func hashTokenIDTestRow(t *testing.T, row Row, contextJSON []byte) []byte {
	t.Helper()
	deltaJSON, err := json.Marshal(row.Delta)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hashRowForEvent(rowHashInput{
		Event: Event{
			Verb: row.Action, EventID: row.EventID,
			Actor:   Actor{Type: actorTypeFromCode(row.ActorKind), ID: row.ActorID},
			Entity:  Entity{Type: row.EntityKind, ID: row.EntityID},
			Context: row.Context, Delta: row.Delta, Outcome: row.Outcome,
			Error: row.Error, Extra: row.Extra, OccurredAt: row.EventTime,
			IdempotencyKey: row.IdempotencyKey,
		},
		EventID: row.EventID, Shard: row.Shard, Seq: row.Seq, ContextJSON: contextJSON,
		DeltaJSON: deltaJSON, LastHash: row.PrevHash, Version: row.HashVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func tokenIDTestRow(orgID uuid.UUID, action string, version int16, tokenID uuid.UUID) Row {
	return Row{
		OrgID: orgID, EventTime: time.Now().UTC().Truncate(time.Microsecond), EventID: uuid.Must(uuid.NewV7()),
		Shard: 1, ActorID: uuid.Must(uuid.NewV7()), ActorKind: 1,
		Action: action, Outcome: OutcomeOK, EntityKind: "node", EntityID: uuid.Must(uuid.NewV7()),
		Context: EventContext{
			OrgID: orgID, RequestID: "req-" + action, Source: SourceMCP, APITokenID: tokenID,
		},
		HashVersion: version,
	}
}

// tokenIDTestContext is every context encoding the ledger's writers used.
type tokenIDTestContext interface {
	contextBeforeTokenID | contextWithOmitemptyTokenID | EventContext
}

func marshalTokenIDTestContext[C tokenIDTestContext](t *testing.T, context C) []byte {
	t.Helper()
	encoded, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// TestVerifyBundleAcceptsEveryTokenIDEncodingTheLedgerWrote pins TACK-514: one
// chain holds rows hashed without the token id key (before TACK-502), with an
// explicit zero token id (f48a5a6 until TACK-514), with a real token id, and
// as the writer encodes a context today. Every row must verify.
func TestVerifyBundleAcceptsEveryTokenIDEncodingTheLedgerWrote(t *testing.T) {
	orgID := uuid.Must(uuid.NewV7())
	tokenID := uuid.Must(uuid.NewV7())
	rows := []Row{
		tokenIDTestRow(orgID, "node.read", auditHashVersion2, uuid.Nil),
		tokenIDTestRow(orgID, "node.update", auditHashVersion3, uuid.Nil),
		tokenIDTestRow(orgID, "node.create", auditHashVersion3, uuid.Nil),
		tokenIDTestRow(orgID, string(VerbAuthTokenUsed), auditHashVersion3, tokenID),
		tokenIDTestRow(orgID, "node.delete", auditHashVersion3, uuid.Nil),
	}
	currentContext := marshalTokenIDTestContext(t, rows[4].Context)
	if strings.Contains(string(currentContext), "api_token_id") {
		t.Fatalf("current context encoding %s carries a zero token id", currentContext)
	}
	contexts := [][]byte{
		marshalTokenIDTestContext(t, contextBeforeTokenIDFrom(rows[0].Context)),
		marshalTokenIDTestContext(t, contextBeforeTokenIDFrom(rows[1].Context)),
		marshalTokenIDTestContext(t, contextWithOmitemptyTokenID{contextBeforeTokenIDFrom(rows[2].Context), uuid.Nil}),
		marshalTokenIDTestContext(t, contextWithOmitemptyTokenID{contextBeforeTokenIDFrom(rows[3].Context), tokenID}),
		currentContext,
	}
	if !strings.Contains(string(contexts[2]), `"api_token_id":"00000000-0000-0000-0000-000000000000"`) {
		t.Fatalf("phase 8 context encoding %s lacks the explicit zero token id", contexts[2])
	}
	rows = chainTokenIDTestRows(t, rows, contexts)
	dir := t.TempDir()
	pub := writeSignedExportTestBundle(t, dir, rows)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(report.ChainBreaks) != 0 {
		t.Fatalf("chain breaks = %v, want none", report.ChainBreaks)
	}
	if report.HashMatches != len(rows) {
		t.Fatalf("hash matches = %d, want %d", report.HashMatches, len(rows))
	}
}

// TestVerifyBundleReportsEditedZeroTokenIDRows proves the explicit zero token
// id candidate accepts no edit: a phase 8 row with a changed action, and one
// whose context gained a forged token id, both stay breaks.
func TestVerifyBundleReportsEditedZeroTokenIDRows(t *testing.T) {
	orgID := uuid.Must(uuid.NewV7())
	rows := []Row{
		tokenIDTestRow(orgID, "node.read", auditHashVersion3, uuid.Nil),
		tokenIDTestRow(orgID, "node.update", auditHashVersion3, uuid.Nil),
	}
	contexts := [][]byte{
		marshalTokenIDTestContext(t, contextWithOmitemptyTokenID{contextBeforeTokenIDFrom(rows[0].Context), uuid.Nil}),
		marshalTokenIDTestContext(t, contextWithOmitemptyTokenID{contextBeforeTokenIDFrom(rows[1].Context), uuid.Nil}),
	}
	rows = chainTokenIDTestRows(t, rows, contexts)
	rows[0].Action = "node.delete"
	rows[1].Context.APITokenID = uuid.Must(uuid.NewV7())
	dir := t.TempDir()
	pub := writeSignedExportTestBundle(t, dir, rows)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.HashMatches != 0 {
		t.Fatalf("hash matches = %d, want 0", report.HashMatches)
	}
	breaks := strings.Join(report.ChainBreaks, "\n")
	for _, row := range rows {
		if !strings.Contains(breaks, row.EventID.String()+" hash mismatch") {
			t.Fatalf("chain breaks = %v, want a hash mismatch for row %s", report.ChainBreaks, row.EventID)
		}
	}
}
