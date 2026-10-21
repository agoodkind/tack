package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// ClickHouseFixture configures one disposable authenticated ClickHouse server.
type ClickHouseFixture struct {
	Endpoint  string
	Username  string
	Password  string
	Container string
}

// ClickHouse starts the stack's native image with an isolated password file.
func ClickHouse(t T) ClickHouseFixture {
	t.Helper()
	skipWhenShort(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	fixture, err := provisionClickHouse(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(t.Output(), "start authenticated ClickHouse: %v\n", err)
		t.FailNow()
	}
	return fixture
}

func provisionClickHouse(ctx context.Context) (ClickHouseFixture, error) {
	image, err := serviceImage(ctx, "clickhouse")
	if err != nil {
		return ClickHouseFixture{}, err
	}
	password, err := randomHex(ctx, 24)
	if err != nil {
		return ClickHouseFixture{}, err
	}
	docker, err := dockerClient(ctx)
	if err != nil {
		return ClickHouseFixture{}, err
	}
	defer docker.Close()
	const username = "tack_audit_consumer"
	const runtimeInputPath = "/run/secrets/CLICKHOUSE_PASSWORD"
	files := make(map[string][]byte)
	files[runtimeInputPath] = []byte(password)
	started, err := startEngine(ctx, docker, engineSpec{
		kind: "clickhouse", image: image, platform: nil, cmd: nil,
		env: []string{
			"CLICKHOUSE_USER=" + username, "CLICKHOUSE_PASSWORD_FILE=" + runtimeInputPath,
			"CLICKHOUSE_DB=audit", "CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=0",
		},
		files: files,
	})
	if err != nil {
		return ClickHouseFixture{}, err
	}
	fixture := ClickHouseFixture{
		Endpoint: "http://" + net.JoinHostPort(started.address, "8123"),
		Username: username, Password: password, Container: started.name,
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fixture.Endpoint+"/ping", nil)
		if err != nil {
			wrapped := fmt.Errorf("build ClickHouse readiness request: %w", err)
			slog.ErrorContext(ctx, "testenv.clickhouse_readiness_failed", slog.String("err", wrapped.Error()))
			return ClickHouseFixture{}, wrapped
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return fixture, nil
			}
		}
		if !sleepOrDone(ctx) {
			wrapped := fmt.Errorf("ClickHouse did not become ready: %w", ctx.Err())
			slog.ErrorContext(ctx, "testenv.clickhouse_readiness_failed", slog.String("err", wrapped.Error()))
			return ClickHouseFixture{}, wrapped
		}
	}
}
