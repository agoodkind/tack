package audit

import (
	"testing"

	"github.com/google/uuid"
)

// TestSystemOrgID checks the reserved system org ID for global operations.
// EventContext.OrgID requires a non-nil UUID. Stored events reference this ID,
// which must remain fixed.
func TestSystemOrgID(t *testing.T) {
	if SystemOrgID() == uuid.Nil {
		t.Fatal("SystemOrgID must not be the nil UUID")
	}

	want := "00000000-0000-0000-0000-0000000005ee"
	if SystemOrgID().String() != want {
		t.Fatalf("SystemOrgID = %s, want %s", SystemOrgID(), want)
	}

	// Each call must return the ID that stored rows already reference.
	if SystemOrgID() != SystemOrgID() {
		t.Fatal("SystemOrgID is not stable between calls")
	}
}

// TestOperatorPrincipalActorType checks that a zero Kind records an operator
// and a service Kind records a service rather than a human.
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
