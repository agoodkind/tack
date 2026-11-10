package audit

import "testing"

// TestActorKindCodeOperator checks that ActorOperator uses ledger code 5.
// The audit.events actor_kind column uses SMALLINT and stores the integer.
// The existing mappings must remain stable:
//
//   - user uses code 1.
//   - service uses code 2.
//   - system uses code 3.
//   - api_token uses code 4.
func TestActorKindCodeOperator(t *testing.T) {
	if got := actorKindCode(ActorOperator); got != 5 {
		t.Fatalf("actorKindCode(ActorOperator) = %d, want 5", got)
	}
}

// TestActorKindCodesStable checks the four existing actor codes.
// Reordering the mappings would change the meaning of stored ledger values.
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
