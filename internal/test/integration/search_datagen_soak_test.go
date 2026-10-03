package integration

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
)

const (
	// soakTestKinds is the number of operation kinds in the soak mix.
	soakTestKinds = 15
	// soakTestOperations is three passes of the soak operation mix.
	soakTestOperations = 3 * soakTestKinds
)

type datagenSoakOutput struct {
	Result struct {
		StopReason          string `json:"stop_reason"`
		Operations          int    `json:"operations"`
		Searches            int    `json:"searches"`
		SearchesUnavailable int    `json:"searches_unavailable"`
		Latency             []struct {
			Kind  string  `json:"kind"`
			Calls int     `json:"calls"`
			MinMs float64 `json:"min_ms"`
			P50Ms float64 `json:"p50_ms"`
			P95Ms float64 `json:"p95_ms"`
			MaxMs float64 `json:"max_ms"`
		} `json:"latency"`
	} `json:"result"`
}

// TestSearchDatagenSoakReportsSearchLatency runs `ops qa datagen soak
// --commit` through the audited command tree against search-enabled engines
// for three passes of the operation mix. The soak stops at its first failed
// operation. The max_ops stop reason after all 45 operations requires zero
// errors. The result must report answered searches, no search refused as
// unavailable, and for every operation kind a positive call count with
// min <= p50 <= p95 <= max and, for 3 to 20 calls, p95 equal to max. A
// second run with public search off must stop at max_ops,
// count every search as unavailable, and record its latency under
// search_unavailable.
func TestSearchDatagenSoakReportsSearchLatency(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	cfg := *fixture.Config
	cfg.DatagenAllowTarget = "local"
	output := runDatagenSoakCommit(t, &cfg)
	result := output.Result
	if result.StopReason != "max_ops" || result.Operations != soakTestOperations {
		t.Fatalf("soak stopped with %q after %d operations, want max_ops after %d", result.StopReason, result.Operations, soakTestOperations)
	}
	if result.Searches == 0 || result.SearchesUnavailable != 0 {
		t.Fatalf("soak searches = %d answered and %d unavailable, want answered searches only", result.Searches, result.SearchesUnavailable)
	}
	calls, searchCalls := 0, 0
	for _, latency := range result.Latency {
		if latency.Calls <= 0 || latency.MinMs < 0 || latency.MinMs > latency.P50Ms ||
			latency.P50Ms > latency.P95Ms || latency.P95Ms > latency.MaxMs {
			t.Fatalf("latency of %s = %+v, want calls above zero and 0 <= min <= p50 <= p95 <= max", latency.Kind, latency)
		}
		// With 3 to 20 calls the nearest-rank p95 is the largest value, also
		// when values tie.
		if latency.Calls >= 3 && latency.Calls <= 20 && latency.P95Ms != latency.MaxMs {
			t.Fatalf("latency of %s = %+v, want p95 equal to max", latency.Kind, latency)
		}
		calls += latency.Calls
		if latency.Kind == "search" {
			searchCalls = latency.Calls
		}
	}
	if calls != result.Operations || searchCalls != result.Searches {
		t.Fatalf("latency covers %d operations and %d searches, want %d and %d: %+v",
			calls, searchCalls, result.Operations, result.Searches, result.Latency)
	}
	t.Logf("soak searches=%d latency=%+v", result.Searches, result.Latency)

	disabled := cfg
	disabled.SearchPublicEnabled = false
	refused := runDatagenSoakCommit(t, &disabled).Result
	wantUnavailable := soakTestOperations / soakTestKinds
	if refused.StopReason != "max_ops" || refused.Operations != soakTestOperations ||
		refused.Searches != 0 || refused.SearchesUnavailable != wantUnavailable {
		t.Fatalf("soak with public search off = %q after %d operations with %d answered and %d unavailable searches, want max_ops after %d with 0 and %d",
			refused.StopReason, refused.Operations, refused.Searches, refused.SearchesUnavailable, soakTestOperations, wantUnavailable)
	}
	unavailableCalls := 0
	for _, latency := range refused.Latency {
		if latency.Kind == "search" {
			t.Fatalf("soak with public search off reported answered search latency %+v", latency)
		}
		if latency.Kind == "search_unavailable" {
			unavailableCalls = latency.Calls
		}
	}
	if unavailableCalls != wantUnavailable {
		t.Fatalf("search_unavailable latency calls = %d, want %d: %+v", unavailableCalls, wantUnavailable, refused.Latency)
	}
}

// runDatagenSoakCommit runs `ops qa datagen soak --commit` once through the
// audited command tree with the real SQL outbox and decodes its result.
func runDatagenSoakCommit(t *testing.T, cfg *config.Config) datagenSoakOutput {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("open the outbox pool: %v", err)
	}
	t.Cleanup(pool.Close)
	output := &bytes.Buffer{}
	factory := cli.System(cfg)
	factory.Out = output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	root := searchCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca33",
		"--operator-email", "operator@example.com", "--operator-name", "Soak Latency Test",
		"ops", "qa", "datagen", "soak", "--commit", "--duration", "10m", "--rate", "1000",
		"--max-ops", strconv.Itoa(soakTestOperations), "--seed", strconv.FormatInt(nextHarnessSeed(), 10),
	})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("ops qa datagen soak --commit: %v\n%s", err, output.String())
	}
	var decoded datagenSoakOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode the soak output %q: %v", output.String(), err)
	}
	return decoded
}
