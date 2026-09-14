package ops

import (
	"context"
	"strings"
	"testing"
)

// TestStaleTserverFlagsReadsTheFileAgainstTheServer pins the comparison the
// deploy relies on: the flag file the stack carries against what the running
// tablet server reports. A value the server holds at the file's setting is
// current; a changed value or a flag the server never read is stale.
func TestStaleTserverFlagsReadsTheFileAgainstTheServer(t *testing.T) {
	file := parseTserverFlagFile([]byte(`
# comment
--ysql_pg_conf_csv=max_connections=100

--memory_limit_hard_bytes=8589934592
--ysql_num_tablets=1
`))
	if len(file) != 3 || file["ysql_pg_conf_csv"] != "max_connections=100" {
		t.Fatalf("parsed flags = %v", file)
	}

	varz := `{"flags":[
		{"name":"ysql_pg_conf_csv","value":"max_connections=100","type":"Custom"},
		{"name":"memory_limit_hard_bytes","value":"8589934592","type":"Custom"},
		{"name":"ysql_num_tablets","value":"-1","type":"Default"},
		{"name":"raft_heartbeat_interval_ms","value":"500","type":"Default"}]}`
	running, err := runningTserverFlags(context.Background(), []byte(varz))
	if err != nil {
		t.Fatalf("runningTserverFlags: %v", err)
	}
	stale := staleTserverFlags(file, running)
	if strings.Join(stale, ",") != "ysql_num_tablets" {
		t.Fatalf("stale = %v, want only the flag the server still holds at its default", stale)
	}

	running["ysql_num_tablets"] = "1"
	if stale := staleTserverFlags(file, running); len(stale) != 0 {
		t.Fatalf("stale = %v after the server picked the file up, want none", stale)
	}

	delete(running, "memory_limit_hard_bytes")
	if stale := staleTserverFlags(file, running); strings.Join(stale, ",") != "memory_limit_hard_bytes" {
		t.Fatalf("stale = %v, want the flag the server never listed", stale)
	}

	if _, err := runningTserverFlags(context.Background(), []byte("not json")); err == nil {
		t.Fatal("a listing that is not JSON must not read as a flag set")
	}
}
