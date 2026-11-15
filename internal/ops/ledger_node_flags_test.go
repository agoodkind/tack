package ops

import (
	"context"
	"strings"
	"testing"
)

// TestStaleTserverFlagsComparesFileAndServer checks matching, changed, and missing flags.
func TestStaleTserverFlagsComparesFileAndServer(t *testing.T) {
	file := parseTserverFlagFile(
		[]byte(
			`
# comment
--ysql_pg_conf_csv=max_connections=100

--memory_limit_hard_bytes=8589934592
--ysql_num_tablets=1
`,
		),
	)
	if len(file) != 3 || file["ysql_pg_conf_csv"] != "max_connections=100" {
		t.Fatalf("parsed flags = %v", file)
	}

	varz := `{"flags":[
		{"name":"ysql_pg_conf_csv","value":"max_connections=100","type":"Custom"},
		{"name":"memory_limit_hard_bytes","value":"8589934592","type":"Custom"},
		{"name":"ysql_num_tablets","value":"-1","type":"Default"},
		{"name":"raft_heartbeat_interval_ms","value":"500","type":"Default"}]}`
	serverFlags, err := runningTserverFlags(context.Background(), []byte(varz))
	if err != nil {
		t.Fatalf("runningTserverFlags: %v", err)
	}

	stale := staleTserverFlags(file, serverFlags)
	if strings.Join(stale, ",") != "ysql_num_tablets" {
		t.Fatalf("stale = %v, want only the flag with the server's default value", stale)
	}

	serverFlags["ysql_num_tablets"] = "1"
	if stale := staleTserverFlags(file, serverFlags); len(stale) != 0 {
		t.Fatalf("stale = %v after the server applied the file values, want an empty list", stale)
	}

	delete(serverFlags, "memory_limit_hard_bytes")
	if stale := staleTserverFlags(file, serverFlags); strings.Join(
		stale,
		",",
	) != "memory_limit_hard_bytes" {
		t.Fatalf("stale = %v, want the flag the server never listed", stale)
	}

	if _, err := runningTserverFlags(context.Background(), []byte("not json")); err == nil {
		t.Fatal("runningTserverFlags accepted invalid JSON")
	}
}
