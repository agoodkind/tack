package main

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestBackfillOptionsFromInput pins the flag parsing: comma-separated UUIDs
// land in the options, whitespace is tolerated, empty flags mean no
// exemptions, and a typo refuses instead of silently narrowing an exemption.
func TestBackfillOptionsFromInput(t *testing.T) {
	orgA := uuid.Must(uuid.NewV7())
	orgB := uuid.Must(uuid.NewV7())
	actor := uuid.Must(uuid.NewV7())

	opts, err := backfillOptionsFromInput(auditBackfillOrgInput{
		AbsorbOrgs:        orgA.String() + ", " + orgB.String(),
		AcknowledgeActors: actor.String(),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(opts.AbsorbOrgs) != 2 || opts.AbsorbOrgs[0] != orgA || opts.AbsorbOrgs[1] != orgB {
		t.Fatalf("absorb orgs = %v, want [%s %s]", opts.AbsorbOrgs, orgA, orgB)
	}
	if len(opts.AcknowledgedActors) != 1 || opts.AcknowledgedActors[0] != actor {
		t.Fatalf("acknowledged actors = %v, want [%s]", opts.AcknowledgedActors, actor)
	}

	empty, err := backfillOptionsFromInput(auditBackfillOrgInput{AbsorbOrgs: "", AcknowledgeActors: " "})
	if err != nil {
		t.Fatalf("empty parse: %v", err)
	}
	if len(empty.AbsorbOrgs) != 0 || len(empty.AcknowledgedActors) != 0 {
		t.Fatalf("empty flags produced exemptions: %+v", empty)
	}

	if _, err := backfillOptionsFromInput(auditBackfillOrgInput{AbsorbOrgs: "not-a-uuid", AcknowledgeActors: ""}); err == nil || !strings.Contains(err.Error(), "absorb-org") {
		t.Fatalf("typo err = %v, want the absorb-org parse refusal", err)
	}
	if _, err := backfillOptionsFromInput(auditBackfillOrgInput{AbsorbOrgs: "", AcknowledgeActors: "nope"}); err == nil || !strings.Contains(err.Error(), "acknowledge-actor") {
		t.Fatalf("typo err = %v, want the acknowledge-actor parse refusal", err)
	}
}
