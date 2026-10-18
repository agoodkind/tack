//go:build searchconversion

package integration_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/testenv"
)

func writeConversionEngine(t *testing.T, directory string, conversion testenv.OpenSearchConversionFixture) {
	t.Helper()
	docker, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	inspected, err := docker.ContainerInspect(t.Context(), conversion.Fixture.Container, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	containers := make([]string, 0, len(conversion.Containers))
	for _, name := range conversion.Containers {
		created, err := docker.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		containers = append(containers, created.Container.ID)
	}
	manifest := struct {
		Container            string   `json:"container"`
		URL                  string   `json:"url"`
		AdminDN              string   `json:"admin_dn"`
		CAFile               string   `json:"ca_file"`
		AdminCertificateFile string   `json:"admin_certificate_file"`
		AdminKeyFile         string   `json:"admin_key_file"`
		Containers           []string `json:"containers"`
	}{
		Container: inspected.Container.ID, URL: "https://localhost:9200",
		AdminDN: testenv.OpenSearchConversionAdministratorDN, CAFile: "ca.crt",
		AdminCertificateFile: "admin.crt", AdminKeyFile: "admin.key", Containers: containers,
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(directory, "engine.json.pending")
	if err := os.WriteFile(pending, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pending, filepath.Join(directory, "engine.json")); err != nil {
		t.Fatal(err)
	}
}

func writeConversionMarker(t *testing.T, directory, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForConversionPhase(t *testing.T, directory, phase string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(filepath.Join(directory, "conversion.failed")); err == nil {
			t.Fatal("the Configs conversion runner failed; inspect its private run log")
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(directory, phase)); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("the Configs conversion runner did not finish %s within five minutes", phase)
		case <-t.Context().Done():
			t.Fatal("the conversion test context ended before the security tasks completed")
		case <-poll.C:
		}
	}
}
