package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// wholeRangeFilter selects one org with both required time bounds.
func wholeRangeFilter(orgID uuid.UUID) QueryFilter {
	return QueryFilter{
		OrgID:     orgID,
		Oldest:    time.Unix(0, 0).UTC(),
		Latest:    time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC),
		Action:    "",
		ActorID:   uuid.Nil,
		EntityID:  uuid.Nil,
		RequestID: "",
		TraceID:   "",
		Limit:     0,
	}
}

// TestQueryFilterRejectsMissingOrg checks that omitting an org fails validation
// rather than selecting the whole ledger.
func TestQueryFilterRejectsMissingOrg(t *testing.T) {
	err := wholeRangeFilter(uuid.Nil).Validate()
	if err == nil || !strings.Contains(err.Error(), "org_id required") {
		t.Fatalf("error = %v, want the missing org refused", err)
	}
}

// TestQueryFilterAcceptsExplicitNilOrg checks validation with explicit NilOrg access.
func TestQueryFilterAcceptsExplicitNilOrg(t *testing.T) {
	filter := wholeRangeFilter(uuid.Nil)
	filter.NilOrg = true
	if err := filter.Validate(); err != nil {
		t.Fatalf("a filter naming the nil org on purpose was refused: %v", err)
	}
}

// TestQueryFilterRejectsNilOrgFlagWithNonNilOrg checks that NilOrg cannot
// bypass validation with a contradictory non-nil org.
func TestQueryFilterRejectsNilOrgFlagWithNonNilOrg(t *testing.T) {
	filter := wholeRangeFilter(uuid.Must(uuid.NewV7()))
	filter.NilOrg = true
	err := filter.Validate()
	if err == nil || !strings.Contains(err.Error(), "nil_org set beside org") {
		t.Fatalf("error = %v, want the contradictory filter refused", err)
	}
}

// TestQueryFilterRequiresTimeBoundsForNilOrg checks that explicit NilOrg
// access does not permit an unbounded scan.
func TestQueryFilterRequiresTimeBoundsForNilOrg(t *testing.T) {
	filter := wholeRangeFilter(uuid.Nil)
	filter.NilOrg = true
	filter.Latest = time.Time{}
	err := filter.Validate()
	if err == nil || !strings.Contains(err.Error(), "oldest and latest required") {
		t.Fatalf("error = %v, want the open-ended range refused", err)
	}
}
