package testenv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestLedgerReleaseEvidenceQueriesEachCapturedContainer(t *testing.T) {
	directory := filepath.Join(".testenv", "ledger-release-"+uuid.NewString())
	t.Setenv(ledgerEvidenceVariable, directory)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		if err := Release(ctx); err != nil {
			t.Errorf("release owned ledgers: %v", err)
		}
	})
	first := Ledger(t)
	if _, err := provisionLedger(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ledgerState.result != first {
		t.Fatal("second ledger changed the cached first connection")
	}
	cli, err := dockerClient(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	owned.Lock()
	names := append([]string(nil), owned.containers...)
	owned.Unlock()
	expected := make(map[string]string)
	for _, name := range names {
		if !strings.HasPrefix(name, "tack-testenv-yugabyte-") {
			continue
		}
		inspected, err := cli.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		address, err := engineAddress(inspected.Container)
		if err != nil {
			t.Fatal(err)
		}
		expected[name] = address
	}
	if len(expected) != 2 {
		t.Fatalf("owned native ledgers=%d, want two", len(expected))
	}
	if err := Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, address := range expected {
		body, err := os.ReadFile(filepath.Join(directory, name, "release.json"))
		if err != nil {
			t.Fatal(err)
		}
		var evidence ledgerReleaseEvidence
		if err := json.Unmarshal(body, &evidence); err != nil {
			t.Fatal(err)
		}
		if evidence.Container != name || !evidence.SQLReady || evidence.SQLAddress != address || len(evidence.Failures) != 0 {
			t.Fatalf("captured ledger %s did not query its actual SQL address %s: %+v", name, address, evidence)
		}
		t.Logf("released native ledger container=%s verified_sql_address=%s evidence=%s", name, evidence.SQLAddress, filepath.Join(directory, name, "release.json"))
	}
}
