package clispec

import (
	"context"
	"encoding/json"
	"testing"

	"goodkind.io/tack/internal/audit"
)

func deployCommitOf(t *testing.T, event audit.Event) (string, bool) {
	t.Helper()
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode event extra %s: %v", event.Extra, err)
	}
	rawCommit, ok := extra["deploy_commit"]
	if !ok {
		return "", false
	}
	var deployCommit string
	if err := json.Unmarshal(rawCommit, &deployCommit); err != nil {
		t.Fatalf("decode deploy commit %s: %v", rawCommit, err)
	}
	return deployCommit, true
}

func TestRunAuditedMutationCarriesDeployCommit(t *testing.T) {
	outbox := &auditTestOutbox{}
	const deployCommit = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true},
		testOperatorSource(), deployCommit, true, outbox, nil,
		func(context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("runAudited: %v", err)
	}
	if len(outbox.events) != 2 {
		t.Fatalf("outbox events = %d, want 2", len(outbox.events))
	}
	for _, event := range outbox.events {
		gotCommit, ok := deployCommitOf(t, event)
		if !ok || gotCommit != deployCommit {
			t.Fatalf("deploy commit = %q, present = %t, want %q", gotCommit, ok, deployCommit)
		}
	}
}

func TestRunAuditedMutationOmitsEmptyDeployCommit(t *testing.T) {
	outbox := &auditTestOutbox{}
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.provision", Mutates: true},
		testOperatorSource(), " \t", true, outbox, nil,
		func(context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("runAudited: %v", err)
	}
	if len(outbox.events) != 2 {
		t.Fatalf("outbox events = %d, want 2", len(outbox.events))
	}
	for _, event := range outbox.events {
		if _, ok := deployCommitOf(t, event); ok {
			t.Fatalf("extra = %s, want deploy_commit omitted", event.Extra)
		}
	}
}
