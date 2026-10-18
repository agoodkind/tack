package integration_test

import (
	_ "embed"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/service_secret_clickhouse_user.sql
var serviceSecretClickHouseUserQuery string

func TestServiceSecretClickHouseRequiresAuthentication(t *testing.T) {
	engine := testenv.ClickHouse(t)
	for _, authenticated := range []bool{true, false} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, engine.Endpoint, strings.NewReader(serviceSecretClickHouseUserQuery))
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			request.SetBasicAuth(engine.Username, engine.Password)
		}
		client := &http.Client{Timeout: time.Minute}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			if response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != engine.Username {
				t.Fatal("ClickHouse refused the consumer's file-backed credential")
			}
			continue
		}
		if response.StatusCode == http.StatusOK || !strings.Contains(strings.ToLower(string(body)), "authentication failed") {
			t.Fatalf("anonymous query returned status %d without an authentication rejection", response.StatusCode)
		}
	}
	docker, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	inspected, err := docker.ContainerInspect(t.Context(), engine.Container, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range inspected.Container.Config.Env {
		if strings.HasPrefix(setting, "CLICKHOUSE_PASSWORD=") {
			t.Fatal("ClickHouse exposes a plaintext password in Docker configuration")
		}
	}
}
