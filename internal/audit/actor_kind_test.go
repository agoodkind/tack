package audit

import "testing"

// TestActorKindCodeOperator pins the operator actor kind to ledger code 5.
// The audit.events actor_kind column is SMALLINT; codes 1 through 4 are
// taken by user, service, system, and api_token, and the mapping must stay
// stable because the ledger stores the integer.
func TestActorKindCodeOperator(t *testing.T) {
	if got := actorKindCode(ActorOperator); got != 5 {
		t.Fatalf("actorKindCode(ActorOperator) = %d, want 5", got)
	}
}

// TestActorKindCodesStable pins the existing four codes so a reorder can
// never silently rewrite history's meaning.
func TestActorKindCodesStable(t *testing.T) {
	cases := []struct {
		actor ActorType
		code  int16
	}{
		{ActorUser, 1},
		{ActorService, 2},
		{ActorSystem, 3},
		{ActorToken, 4},
	}
	for _, c := range cases {
		if got := actorKindCode(c.actor); got != c.code {
			t.Fatalf("actorKindCode(%s) = %d, want %d", c.actor, got, c.code)
		}
	}
}
