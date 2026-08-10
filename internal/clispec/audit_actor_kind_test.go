package clispec

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// TestRunAuditedRecordsPrincipalActorKind pins that the gate records the actor
// kind the identity source resolved: a service principal lands as a service
// actor, and the human sources, which leave Kind at its zero value, still land
// as operator.
func TestRunAuditedRecordsPrincipalActorKind(t *testing.T) {
	cases := []struct {
		name string
		kind audit.ActorType
		want audit.ActorType
	}{
		{name: "service principal", kind: audit.ActorService, want: audit.ActorService},
		{name: "zero kind stays operator", kind: "", want: audit.ActorOperator},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			outbox := &auditTestOutbox{}
			source := auditTestSource{principal: audit.OperatorPrincipal{
				ID:     uuid.MustParse("019dd226-440e-729a-a442-281aaf73ca30"),
				Email:  "",
				Name:   "tack-app",
				Source: "service",
				Kind:   testCase.kind,
			}}
			err := runAudited(
				context.Background(),
				audit.Spec{Verb: "server.serve", Mutates: true},
				source,
				"",
				true,
				outbox,
				nil,
				func(context.Context) error { return nil },
			)
			if err != nil {
				t.Fatalf("runAudited: %v", err)
			}
			if len(outbox.events) != 2 {
				t.Fatalf("events = %d, want intent and outcome", len(outbox.events))
			}
			for _, event := range outbox.events {
				if event.Actor.Type != testCase.want {
					t.Fatalf("actor type = %q, want %q", event.Actor.Type, testCase.want)
				}
			}
		})
	}
}
