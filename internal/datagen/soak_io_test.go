package datagen

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"goodkind.io/tack/internal/clock"
)

func TestSoakContinuesAfterProjectReadFails(t *testing.T) {
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeRerunResult(writer, "project disappeared", true)
	})
	projectID := deterministicUUID("disappeared-project").String()
	project := &soakProject{
		Workspace: testSoakWorkspace("workspace"),
		Reference: projectID,
		RawID:     projectID,
	}
	soak := &Soak{
		driver:   NewDriver(testGraph(handler), false, 777),
		projects: []*soakProject{project},
		options:  SoakOptions{Rate: 1_000_000, MaxOps: soakOperationKinds},
		summary:  SoakSummary{Operations: soakOperationKinds - 1},
		clock:    clock.Wall{},
	}

	summary, err := soak.Run(t.Context(), t.Context(), nil)
	if err != nil {
		t.Fatalf("Soak.Run() error = %v", err)
	}
	if summary.Operations != soakOperationKinds || summary.Reads != 0 {
		t.Fatalf("Soak.Run() summary = %#v", summary)
	}
	logs := output.String()
	if !strings.Contains(logs, "qa.datagen.soak.discovery_skipped") ||
		!strings.Contains(logs, projectID) {
		t.Fatalf("logs = %q, want skipped project warning", logs)
	}
}
