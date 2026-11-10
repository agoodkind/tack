package audit

import (
	"testing"

	"github.com/google/uuid"
)

// TestSystemOrgID checks the reserved organization ID for global operator
// commands. EventContext.OrgID requires a non-nil UUID, and the reader rejects
// uuid.Nil. The ID must stay fixed after events reference it.
func TestSystemOrgID(t *testing.T) {
	if SystemOrgID() == uuid.Nil {
		t.Fatal("SystemOrgID must not be the nil UUID")
	}

	want := "00000000-0000-0000-0000-0000000005ee"
	if SystemOrgID().String() != want {
		t.Fatalf("SystemOrgID = %s, want %s", SystemOrgID(), want)
	}

	// Repeated calls must return the same ID. Changing the value between calls
	// would assign later records to another organization's hash chain.
	if SystemOrgID() != SystemOrgID() {
		t.Fatal("SystemOrgID is not stable between calls")
	}
}

// TestSpecZeroValueHasNoClaims checks that the zero Spec does not declare a
// verb or behavior flags.
func TestSpecZeroValueHasNoClaims(t *testing.T) {
	var spec Spec
	if spec.Verb != "" {
		t.Fatalf("zero Spec verb = %q, want empty", spec.Verb)
	}

	if spec.Mutates || spec.Atomic || spec.Reads || spec.CreatesAuditInfrastructure {
		t.Fatal("zero Spec must not claim any behavior flags")
	}
}

// TestOperatorPrincipalActorType checks that zero Kind selects ActorOperator
// and ActorService selects a service actor. Event builders must preserve that
// distinction instead of recording service runs as human runs.
func TestOperatorPrincipalActorType(t *testing.T) {
	var human OperatorPrincipal
	if human.ActorType() != ActorOperator {
		t.Fatalf("zero-kind principal = %q, want %q", human.ActorType(), ActorOperator)
	}

	service := OperatorPrincipal{Kind: ActorService}
	if service.ActorType() != ActorService {
		t.Fatalf("service principal = %q, want %q", service.ActorType(), ActorService)
	}
}
