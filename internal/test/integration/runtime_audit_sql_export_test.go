package integration_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// requireExportWithout exports orgID between oldest and latest, verifies the
// bundle, and fails if any bundle file contains a value in personal.
func requireExportWithout(t *testing.T, stack runtimeAuditStack, orgID uuid.UUID, oldest, latest time.Time, personal []string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey error = %v, want nil", err)
	}
	dir := t.TempDir()
	filter := audit.QueryFilter{
		OrgID: orgID, Oldest: oldest, Latest: latest, Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil,
		RequestID: "", TraceID: "", Limit: 0,
	}
	if _, err := audit.Export(t.Context(), stack.reader, private, audit.KeyIdentifier(public), filter, dir); err != nil {
		t.Fatalf("Export for orgID %s: error = %v, want nil", orgID, err)
	}
	report, err := audit.VerifyBundle(dir, public)
	if err != nil {
		t.Fatalf("VerifyBundle for orgID %s: error = %v, want nil", orgID, err)
	}
	if report.RowsScanned == 0 || report.Err() != nil {
		t.Fatalf("VerifyBundle for orgID %s: report.RowsScanned = %d, report.Err() = %v; want report.RowsScanned > 0 and report.Err() = nil", orgID, report.RowsScanned, report.Err())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("os.ReadDir error = %v, want nil", err)
	}
	for _, entry := range entries {
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("os.ReadFile for entry.Name() %s: error = %v, want nil", entry.Name(), err)
		}
		for _, value := range personal {
			if strings.Contains(string(content), value) {
				t.Fatalf("export file %s contains a value in personal, want strings.Contains(string(content), value) = false for every value in personal", entry.Name())
			}
		}
	}
}
