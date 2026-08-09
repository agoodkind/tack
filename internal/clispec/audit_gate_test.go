package clispec

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
)

func TestRunAuditedUnresolvableIdentityAbortsInBothModes(t *testing.T) {
	for _, execute := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry-run", true: "execute"}[execute], func(t *testing.T) {
			runCount := 0
			outbox := &auditTestOutbox{failAt: -1}
			err := runAudited(
				context.Background(),
				audit.Spec{Verb: "ops.test", Reads: true},
				auditTestSource{err: errors.New("identity unavailable")},
				execute,
				outbox,
				func(context.Context) error {
					runCount++
					return nil
				},
			)
			if err == nil {
				t.Fatal("runAudited succeeded without an identity")
			}
			if runCount != 0 || len(outbox.events) != 0 {
				t.Fatalf("run count = %d, events = %d", runCount, len(outbox.events))
			}
		})
	}
}

func TestRunAuditedDryRunRunsNothingAndRecordsNothing(t *testing.T) {
	outbox := &auditTestOutbox{failAt: -1}
	output := &bytes.Buffer{}
	runCount := 0
	err := runAudited(
		withDryRunOutput(context.Background(), output),
		audit.Spec{Verb: "ops.test", Mutates: true},
		testOperatorSource(),
		false,
		outbox,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runAudited: %v", err)
	}
	if runCount != 0 || len(outbox.events) != 0 {
		t.Fatalf("run count = %d, events = %d", runCount, len(outbox.events))
	}
	if !strings.Contains(output.String(), "operator: id=") ||
		!strings.Contains(output.String(), "would run: ops.test") {
		t.Fatalf("dry-run output = %q", output.String())
	}
}

func TestRunAuditedReadWriteFailureNeverRuns(t *testing.T) {
	outbox := &auditTestOutbox{failAt: 0}
	runCount := 0
	err := runAudited(
		context.Background(),
		audit.Spec{Verb: "ops.inspect", Reads: true},
		testOperatorSource(),
		true,
		outbox,
		func(context.Context) error {
			runCount++
			return nil
		},
	)
	if err == nil {
		t.Fatal("runAudited succeeded after access-event failure")
	}
	if runCount != 0 {
		t.Fatalf("run count = %d, want 0", runCount)
	}
	if len(outbox.events) != 0 {
		t.Fatalf("outbox events = %d, want 0", len(outbox.events))
	}
}

// TestRunAuditedNilOutboxNeverRuns pins the wiring, not the logic. Nothing in
// the gate itself populates the outbox; a caller has to hand one in. If that
// wiring is ever dropped, every audited command aborts at runtime, and the
// only thing standing between that regression and a release is this test.
func TestRunAuditedNilOutboxNeverRuns(t *testing.T) {
	for _, spec := range []audit.Spec{
		{Verb: "ops.test", Mutates: true},
		{Verb: "ops.inspect", Reads: true},
	} {
		t.Run(spec.Verb, func(t *testing.T) {
			runCount := 0
			err := runAudited(
				context.Background(),
				spec,
				testOperatorSource(),
				true,
				nil,
				func(context.Context) error {
					runCount++
					return nil
				},
			)
			if err == nil {
				t.Fatal("runAudited succeeded with no outbox wired")
			}
			if !strings.Contains(err.Error(), "outbox") {
				t.Fatalf("error = %v, want it to name the missing outbox", err)
			}
			if runCount != 0 {
				t.Fatalf("run count = %d, want the command never invoked", runCount)
			}
		})
	}
}
