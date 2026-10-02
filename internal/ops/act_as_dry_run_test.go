package ops

import (
	"bytes"
	"strings"
	"testing"
)

// TestActAsCreateDryRunRecordsNothing requires the dry run to report the user
// and the org without recording a grant row or making a write.
func TestActAsCreateDryRunRecordsNothing(t *testing.T) {
	f := newActAsFixture(t, humanOperatorFlags())
	sink := &bufferSink{buf: &bytes.Buffer{}}
	if err := runActAsCreate(t.Context(), f.deps, actAsInput(f, f.user.Email, "why"), sink, false); err != nil {
		t.Fatalf("runActAsCreate: %v", err)
	}
	if rows := actAsGrantRows(t, f); len(rows) != 0 || len(f.creator.calls) != 0 {
		t.Fatalf("the dry run recorded %d grant rows and made %d writes, want none", len(rows), len(f.creator.calls))
	}
	if !strings.Contains(sink.buf.String(), f.orgID.String()) || !strings.Contains(sink.buf.String(), `"dry_run":true`) {
		t.Fatalf("report = %s, want the org and dry_run true", sink.buf.String())
	}
}
