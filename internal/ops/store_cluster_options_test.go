package ops

import (
	"slices"
	"testing"

	"goodkind.io/tack/internal/config"
)

// storeClusterDirectoryVariable is the setting the one-shot's bind follows.
const storeClusterDirectoryVariable = "TACK_OPS_FDB_CLUSTER_DIR"

// TestStoreCLIOptionsMountTheConfiguredClusterDirectory requires the fdbcli
// one-shot to mount /etc/foundationdb and read /etc/foundationdb/fdb.cluster
// when the setting is unset, and to mount a configured directory at the same
// path inside the one-shot.
func TestStoreCLIOptionsMountTheConfiguredClusterDirectory(t *testing.T) {
	cfg := &config.Config{BackupFDBImage: "foundationdb/foundationdb:7.4.6", BackupFDBNetwork: "tack_default"}
	cases := []struct {
		name      string
		directory string
		wantBind  string
	}{
		{name: "unset", directory: "", wantBind: "/etc/foundationdb:/etc/foundationdb"},
		{name: "configured", directory: "/srv/tack/fdb", wantBind: "/srv/tack/fdb:/etc/foundationdb"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(storeClusterDirectoryVariable, testCase.directory)
			options, err := storeCLIOptions(t.Context(), cfg, "status minimal")
			if err != nil {
				t.Fatalf("storeCLIOptions: %v", err)
			}
			if !slices.Equal(options.Binds, []string{testCase.wantBind}) {
				t.Fatalf("binds = %v, want [%s]", options.Binds, testCase.wantBind)
			}
			wantCmd := []string{"-C", "/etc/foundationdb/fdb.cluster", "--timeout", "30", "--exec", "status minimal"}
			if !slices.Equal(options.Cmd, wantCmd) {
				t.Fatalf("cmd = %v, want %v", options.Cmd, wantCmd)
			}
			if !slices.Equal(options.Env, []string{"FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster"}) {
				t.Fatalf("env = %v, want FDB_CLUSTER_FILE=/etc/foundationdb/fdb.cluster", options.Env)
			}
		})
	}
}

// TestStoreCLIOptionsRefuseARelativeClusterDirectory requires a relative
// directory to fail before any one-shot is built.
func TestStoreCLIOptionsRefuseARelativeClusterDirectory(t *testing.T) {
	t.Setenv(storeClusterDirectoryVariable, "fdb")
	if _, err := storeCLIOptions(t.Context(), &config.Config{}, "status minimal"); err == nil {
		t.Fatal("storeCLIOptions accepted a relative cluster file directory")
	}
}
