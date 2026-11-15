package postgres

import (
	"testing"
	"time"
)

// The driver must consume connect_timeout instead of sending a startup
// parameter that the PostgreSQL-compatible server could reject.
func TestPoolConfigConsumesConnectTimeoutWithoutForwardingItToTheServer(t *testing.T) {
	// Deployment uses a URL for the app and keyword form for the ops sidecar.
	testCases := []struct {
		name string
		dsn  string
	}{
		{
			name: "app URL form",
			dsn:  deployDSN(2, "yb1:5433", "yb2:5433", "yb3:5433"),
		},
		{
			name: "ops keyword form",
			dsn: "host=yb1,yb2,yb3 port=5433 user=tack dbname=tack " +
				"sslmode=disable connect_timeout=2",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configuration := configuredPool(t, testCase.dsn)
			if configuration.ConnConfig.ConnectTimeout != 2*time.Second {
				t.Fatalf("ConnectTimeout = %s, want %s", configuration.ConnConfig.ConnectTimeout, 2*time.Second)
			}
			for name, value := range configuration.ConnConfig.RuntimeParams {
				t.Errorf("driver sent DSN key %q=%q as a server startup parameter", name, value)
			}
			if len(configuration.ConnConfig.Fallbacks) != 2 {
				t.Fatalf("fallback hosts = %d, want 2", len(configuration.ConnConfig.Fallbacks))
			}
		})
	}
}

// The per-connection ping timeout must bound a scan of every dead connection.
// Driver cleanup can occupy pool slots after a timeout; health probes use a
// fresh dial rather than waiting for those slots.
func TestPoolConfigBoundsTheFullPoolWalkUnderTheHealthDeadline(t *testing.T) {
	configuration := configuredPool(t, deployDSN(2, "yb1:5433"))
	if configuration.PingTimeout <= 0 {
		t.Fatal("PingTimeout is unset; silent connections use the caller deadline")
	}
	if configuration.PingTimeout != acquirePingTimeoutFor(configuration.MaxConns) {
		t.Fatalf("PingTimeout = %s, want %s for a pool of %d", configuration.PingTimeout,
			acquirePingTimeoutFor(configuration.MaxConns), configuration.MaxConns)
	}
	if walk := time.Duration(configuration.MaxConns) * configuration.PingTimeout; walk > acquireWalkBudget {
		t.Fatalf("scanning all %d dead connections takes %s, past the %s scan budget",
			configuration.MaxConns, walk, acquireWalkBudget)
	}
	// Small pools use the ceiling, larger pools divide the budget, and the
	// largest pools use the floor.
	for _, testCase := range []struct {
		maxConns int32
		want     time.Duration
	}{
		{maxConns: 1, want: acquirePingCeiling},
		{maxConns: 4, want: acquirePingCeiling},
		{maxConns: 8, want: acquireWalkBudget / 8},
		{maxConns: 16, want: acquireWalkBudget / 16},
		{maxConns: 128, want: acquirePingFloor},
	} {
		if got := acquirePingTimeoutFor(testCase.maxConns); got != testCase.want {
			t.Errorf("acquirePingTimeoutFor(%d) = %s, want %s", testCase.maxConns, got, testCase.want)
		}
		if walk := time.Duration(testCase.maxConns) * acquirePingTimeoutFor(
			testCase.maxConns,
		); testCase.maxConns <= 64 && walk > acquireWalkBudget {
			t.Errorf("pool of %d scans dead connections in %s, past the %s budget", testCase.maxConns, walk, acquireWalkBudget)
		}
	}
}
