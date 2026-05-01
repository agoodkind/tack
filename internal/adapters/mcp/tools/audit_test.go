package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

func TestRenderAuditRowsShowsCorrelation(t *testing.T) {
	contextJSON, err := json.Marshal(audit.EventContext{
		RequestID: "req-render",
		TraceID:   "trace-render",
		Source:    audit.SourceMCP,
	})
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	row := audit.Row{
		EventTime:  time.Unix(10, 0).UTC(),
		ActorID:    uuid.Must(uuid.NewV7()),
		Action:     "node.read",
		EntityKind: "node",
		EntityID:   uuid.Must(uuid.NewV7()),
		Context:    contextJSON,
		Seq:        1,
	}

	out := renderAuditRows([]audit.Row{row})

	for _, want := range []string{"request", "trace", "req-render", "trace-render"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered audit rows missing %q:\n%s", want, out)
		}
	}
}
