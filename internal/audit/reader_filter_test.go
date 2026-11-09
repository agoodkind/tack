package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// wholeRangeFilter returns the export filter for one organization with the
// required time bounds. It leaves optional filters and the limit unset.
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

// TestQueryFilterRefusesAForgottenOrg checks that a missing organization
// still fails validation. NilOrg must not permit an accidental read without
// an organization choice.
func TestQueryFilterRefusesAForgottenOrg(t *testing.T) {
	err := wholeRangeFilter(uuid.Nil).Validate()
	if err == nil || !strings.Contains(err.Error(), "org_id required") {
		t.Fatalf("error = %v, want the missing org refused", err)
	}
}

// TestQueryFilterServesTheNilOrgWhenNamed checks that an explicit NilOrg
// choice passes validation. The nil organization stores historical rows with
// the same chain structure as other organizations.
func TestQueryFilterServesTheNilOrgWhenNamed(t *testing.T) {
	filter := wholeRangeFilter(uuid.Nil)
	filter.NilOrg = true
	if err := filter.Validate(); err != nil {
		t.Fatalf("a filter naming the nil org on purpose was refused: %v", err)
	}
}

// TestQueryFilterRefusesTheNilOrgFlagBesideARealOrg checks that NilOrg fails
// beside a non-nil OrgID. The flag selects the nil organization and must not
// disable organization validation.
func TestQueryFilterRefusesTheNilOrgFlagBesideARealOrg(t *testing.T) {
	filter := wholeRangeFilter(uuid.Must(uuid.NewV7()))
	filter.NilOrg = true
	err := filter.Validate()
	if err == nil || !strings.Contains(err.Error(), "nil_org set beside org") {
		t.Fatalf("error = %v, want the contradictory filter refused", err)
	}
}

// TestQueryFilterStillRequiresBothTimeBounds checks that NilOrg does not
// permit a read without both time bounds.
func TestQueryFilterStillRequiresBothTimeBounds(t *testing.T) {
	filter := wholeRangeFilter(uuid.Nil)
	filter.NilOrg = true
	filter.Latest = time.Time{}
	err := filter.Validate()
	if err == nil || !strings.Contains(err.Error(), "oldest and latest required") {
		t.Fatalf("error = %v, want the open-ended range refused", err)
	}
}
