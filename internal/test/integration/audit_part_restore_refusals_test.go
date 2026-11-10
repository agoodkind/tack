package integration_test

import (
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/testenv"
)

const (
	partRestoreTargetFragment = "target event does not match"
	partRestoreSourceFragment = "source event does not match"
)

type partRestoreRefusal struct {
	name     string
	prepare  func(t *testing.T, fixture partRestoreRawFixture)
	mutate   func(call *partRestoreCall)
	fragment string
}

type pinnedFieldMutator struct {
	name   string
	target func(call *partRestoreCall)
	source func(call *partRestoreCall)
}

func pinnedFieldMutators() []pinnedFieldMutator {
	return []pinnedFieldMutator{
		{
			name:   "event",
			target: func(c *partRestoreCall) { c.event = uuid.Must(uuid.NewV7()) },
			source: func(c *partRestoreCall) { c.source = uuid.Must(uuid.NewV7()) },
		},
		{
			name:   "row hash",
			target: func(c *partRestoreCall) { c.rowHash = []byte("different") },
			source: func(c *partRestoreCall) { c.sourceRowHash = []byte("different") },
		},
		{
			name:   "seq",
			target: func(c *partRestoreCall) { c.seq++ },
			source: func(c *partRestoreCall) { c.sourceSeq++ },
		},
		{
			name:   "shard",
			target: func(c *partRestoreCall) { c.shard++ },
			source: func(c *partRestoreCall) { c.sourceShard++ },
		},
	}
}

func pinnedFieldRefusals() []partRestoreRefusal {
	refusals := []partRestoreRefusal{}

	for _, field := range pinnedFieldMutators() {
		refusals = append(
			refusals,
			partRestoreRefusal{
				name:     "target " + field.name + " differs",
				mutate:   field.target,
				fragment: partRestoreTargetFragment,
			},
			partRestoreRefusal{
				name:     "source " + field.name + " differs",
				mutate:   field.source,
				fragment: partRestoreSourceFragment,
			},
		)
	}

	return refusals
}

func storedStateRefusals() []partRestoreRefusal {
	return []partRestoreRefusal{
		{
			name: "target already has a part",
			prepare: func(t *testing.T, f partRestoreRawFixture) {
				mustExec(
					t,
					f.admin,
					readTestSQL(t, "part_restore_part_insert.sql"),
					f.org,
					f.target,
					f.call.root,
					f.payload,
				)
			},
			fragment: "already has",
		},
		{
			name: "source part was deleted",
			prepare: func(t *testing.T, f partRestoreRawFixture) {
				mustExec(
					t,
					f.admin,
					readTestSQL(t, "manifest_test_delete_part.sql"),
					f.org,
					f.source,
				)
			},
			fragment: "does not store part 0",
		},
		{
			name: "source payload was altered",
			prepare: func(t *testing.T, f partRestoreRawFixture) {
				mustExec(
					t,
					f.admin,
					readTestSQL(t, "part_restore_update_part_payload.sql"),
					f.org,
					f.source,
					[]byte("altered payload"),
				)
			},
			fragment: "does not hash to the declared root",
		},
		{
			name: "source digest differs",
			prepare: func(t *testing.T, f partRestoreRawFixture) {
				mustExec(
					t,
					f.admin,
					readTestSQL(t, "part_restore_update_part_digest.sql"),
					f.org,
					f.source,
					"00"+f.call.root[2:],
				)
			},
			fragment: "does not hash to the declared root",
		},
		{
			name: "only the source belongs to another organization",
			prepare: func(t *testing.T, f partRestoreRawFixture) {
				mustExec(
					t,
					f.admin,
					readTestSQL(t, "part_restore_update_event_org.sql"),
					f.org,
					f.source,
					uuid.Must(uuid.NewV7()),
				)
			},
			fragment: partRestoreSourceFragment,
		},
		{
			name: "source declares another root",
			prepare: func(t *testing.T, f partRestoreRawFixture) {
				mustExec(
					t,
					f.admin,
					readTestSQL(t, "part_restore_update_event_root.sql"),
					f.org,
					f.source,
					"00"+f.call.root[2:],
				)
			},
			fragment: partRestoreSourceFragment,
		},
		{
			name: "source declares two parts",
			prepare: func(t *testing.T, f partRestoreRawFixture) {
				mustExec(
					t,
					f.admin,
					readTestSQL(t, "part_restore_update_event_parts.sql"),
					f.org,
					f.source,
				)
			},
			fragment: partRestoreSourceFragment,
		},
	}
}

func TestAuditPartRestoreRefusals(t *testing.T) {
	admin := ledgerAdminPool(t)
	operator := rolePool(t, testenv.Ledger(t), "audit_operator")
	otherOrg := uuid.Must(uuid.NewV7())

	cases := append(pinnedFieldRefusals(), storedStateRefusals()...)

	cases = append(
		cases,
		partRestoreRefusal{
			name:     "source is the target",
			mutate:   func(c *partRestoreCall) { c.source = c.event },
			fragment: "source event is the target",
		},
		partRestoreRefusal{
			name:     "target in another org",
			mutate:   func(c *partRestoreCall) { c.org = otherOrg },
			fragment: partRestoreTargetFragment,
		},
		partRestoreRefusal{
			name:     "root differs from the declaration",
			mutate:   func(c *partRestoreCall) { c.root = "00" + c.root[2:] },
			fragment: partRestoreTargetFragment,
		},
	)
	for _, current := range cases {
		t.Run(
			current.name,
			func(t *testing.T) {
				fixture := newPartRestoreRawFixture(t, admin)
				if current.prepare != nil {
					current.prepare(t, fixture)
				}

				call := fixture.call
				if current.mutate != nil {
					current.mutate(&call)
				}

				before := readPartRestoreCounts(t, admin, call.changeID)
				requirePartRestoreRefusal(t, call.run(t, operator), current.fragment)

				if after := readPartRestoreCounts(t, admin, call.changeID); after != before {
					t.Fatalf("counts after a refusal = %+v, want %+v", after, before)
				}
			},
		)
	}
}

func TestAuditPartRestoreDuplicateChangeEventRollsBackPart(t *testing.T) {
	admin := ledgerAdminPool(t)
	operator := rolePool(t, testenv.Ledger(t), "audit_operator")
	fixture := newPartRestoreRawFixture(t, admin)
	mustExec(t, admin, readTestSQL(t, "part_restore_outbox_duplicate.sql"), fixture.call.changeID)
	before := readPartRestoreCounts(t, admin, fixture.call.changeID)
	requirePGCode(t, fixture.call.run(t, operator), "23505")
	if after := readPartRestoreCounts(t, admin, fixture.call.changeID); after != before {
		t.Fatalf(
			"counts after the duplicate error = %+v, want %+v (no part and no second outbox row)",
			after,
			before,
		)
	}
}
