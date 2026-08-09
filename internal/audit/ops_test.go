package audit

import (
	"testing"

	"github.com/google/uuid"
)

// TestSystemOrgID pins the reserved system org id. Global ops record on this
// org; EventContext.OrgID is mandatory and the reader rejects uuid.Nil, so
// the constant must be a fixed non-nil UUID that never changes once events
// reference it.
func TestSystemOrgID(t *testing.T) {
	if SystemOrgID == uuid.Nil {
		t.Fatal("SystemOrgID must not be uuid.Nil")
	}
	want := "00000000-0000-0000-0000-0000000005ee"
	if SystemOrgID.String() != want {
		t.Fatalf("SystemOrgID = %s, want %s", SystemOrgID, want)
	}
}

// TestSpecZeroValueHasNoClaims confirms the zero value carries no declaration.
func TestSpecZeroValueHasNoClaims(t *testing.T) {
	var spec Spec
	if spec.Verb != "" {
		t.Fatalf("zero AuditSpec verb = %q, want empty", spec.Verb)
	}
	if spec.Mutates || spec.Atomic || spec.Reads {
		t.Fatal("zero AuditSpec must not claim any behavior flags")
	}
}
