package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// wholeRangeFilter is the export's filter for one org, with the time bounds
// the guard requires and nothing else set.
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

// TestQueryFilterRefusesAForgottenOrg pins the guard the nil-org flag must not
// weaken: a filter whose org was never set is still refused, because that is
// what an accidental read of the whole ledger looks like.
func TestQueryFilterRefusesAForgottenOrg(t *testing.T) {
	err := wholeRangeFilter(uuid.Nil).Validate()
	if err == nil || !strings.Contains(err.Error(), "org_id required") {
		t.Fatalf("error = %v, want the missing org refused", err)
	}
}

// TestQueryFilterServesTheNilOrgWhenNamed pins the other half: a caller that
// names the nil org on purpose is served, because the ledger holds rows under
// it and their chain is a chain like any other.
func TestQueryFilterServesTheNilOrgWhenNamed(t *testing.T) {
	filter := wholeRangeFilter(uuid.Nil)
	filter.NilOrg = true
	if err := filter.Validate(); err != nil {
		t.Fatalf("a filter naming the nil org on purpose was refused: %v", err)
	}
}

// TestQueryFilterRefusesTheNilOrgFlagBesideARealOrg pins that the flag is a
// name for one org rather than a switch that turns the guard off: set beside a
// real org it contradicts the filter, and a contradiction is refused.
func TestQueryFilterRefusesTheNilOrgFlagBesideARealOrg(t *testing.T) {
	filter := wholeRangeFilter(uuid.Must(uuid.NewV7()))
	filter.NilOrg = true
	err := filter.Validate()
	if err == nil || !strings.Contains(err.Error(), "nil_org set beside org") {
		t.Fatalf("error = %v, want the contradictory filter refused", err)
	}
}

// TestQueryFilterStillRequiresBothTimeBounds pins that naming the nil org does
// not relax the range rule an unbounded scan is refused by.
func TestQueryFilterStillRequiresBothTimeBounds(t *testing.T) {
	filter := wholeRangeFilter(uuid.Nil)
	filter.NilOrg = true
	filter.Latest = time.Time{}
	err := filter.Validate()
	if err == nil || !strings.Contains(err.Error(), "oldest and latest required") {
		t.Fatalf("error = %v, want the open-ended range refused", err)
	}
}
