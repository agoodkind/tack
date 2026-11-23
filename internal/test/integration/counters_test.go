package integration

import (
	"testing"
)

func TestAllocateSequenceByKeyReturnsConsecutiveValues(t *testing.T) {
	env := SetupTestEnv(t)

	for want := int64(1); want <= 3; want++ {
		got, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
		if err != nil {
			t.Fatalf("AllocateSequenceByKey: %v", err)
		}

		if got != want {
			t.Fatalf("sequence = %d, want %d", got, want)
		}
	}
}

func TestAllocateSequenceByKeySeparatesKeys(t *testing.T) {
	env := SetupTestEnv(t)
	first, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-issue-")
	if err != nil {
		t.Fatalf("allocate issue sequence: %v", err)
	}

	second, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-epic-")
	if err != nil {
		t.Fatalf("allocate epic sequence: %v", err)
	}

	if first != 1 || second != 1 {
		t.Fatalf("sequences = %d, %d, want independent first values", first, second)
	}
}

func TestSeedSequenceByKeyRaisesCounter(t *testing.T) {
	env := SetupTestEnv(t)
	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 40); err != nil {
		t.Fatalf("SeedSequenceByKey: %v", err)
	}

	got, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("AllocateSequenceByKey: %v", err)
	}

	if got != 41 {
		t.Fatalf("sequence = %d, want 41", got)
	}
}

func TestSeedSequenceByKeyNeverLowersCounter(t *testing.T) {
	env := SetupTestEnv(t)
	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 40); err != nil {
		t.Fatalf("seed counter to 40: %v", err)
	}

	if err := env.Stores.Nodes.SeedSequenceByKey(env.Ctx, env.OrgID, "FAN-", 10); err != nil {
		t.Fatalf("seed counter to 10: %v", err)
	}

	first, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}

	second, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, "FAN-")
	if err != nil {
		t.Fatalf("second allocation: %v", err)
	}

	if first != 41 || second != 42 {
		t.Fatalf("sequences = %d, %d, want 41, 42", first, second)
	}
}
