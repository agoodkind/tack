package ops

import (
	"bytes"
	"strings"
	"testing"
)

// TestActAsCreateDryRunRecordsNothing checks the organization and dry_run:true
// in the report and checks that the command does not record a grant or user
// audit event or create an issue. The command also reports the user; this test
// does not assert that field.
func TestActAsCreateDryRunRecordsNothing(t *testing.T) {
	f := newActAsFixture(t, humanOperatorFlags())
	sink := &bufferSink{buf: &bytes.Buffer{}}
	if err := runActAsCreate(
		t.Context(),
		f.deps,
		actAsInput(f, f.user.Email, "why"),
		sink,
		false,
	); err != nil {
		t.Fatalf("runActAsCreate: %v", err)
	}

	requireNoActAsWrite(t, f)
	if !strings.Contains(sink.buf.String(), f.orgID.String()) ||
		!strings.Contains(sink.buf.String(), `"dry_run":true`) {
		t.Fatalf("report = %s, want the org and dry_run true", sink.buf.String())
	}
}
