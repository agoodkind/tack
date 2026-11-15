package integration_test

import "testing"

// requirePermanentMirrorAudit checks that internal replacement work does not record compliance events.
func requirePermanentMirrorAudit(t *testing.T, env *migrationEnv, _ string) {
	t.Helper()
	if events := searchWorkerEvents(t, env.Stores, nil); len(events) != 0 {
		t.Fatalf("internal replacement recorded %d compliance events", len(events))
	}
}
