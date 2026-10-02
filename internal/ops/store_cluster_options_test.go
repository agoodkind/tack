package ops

import (
	"os"
	"slices"
	"testing"

	"goodkind.io/tack/internal/config"
)

// storeClusterDirectoryVariable is the setting the one-shot's bind follows.
const storeClusterDirectoryVariable = "TACK_OPS_FDB_CLUSTER_DIR"

// loadStoreTestConfig loads the server configuration with the cluster file
// directory set to directory, or left unset when directory is empty.
func loadStoreTestConfig(t *testing.T, directory string) (*config.Config, error) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://store-cluster-test@localhost/tack")
	// t.Setenv restores the variable at cleanup; Unsetenv then removes it so
	// the default applies.
	t.Setenv(storeClusterDirectoryVariable, directory)
	if directory == "" {
		if err := os.Unsetenv(storeClusterDirectoryVariable); err != nil {
			t.Fatalf("unset %s: %v", storeClusterDirectoryVariable, err)
		}
	}
	return config.Load()
}

// TestStoreCLIOptionsMountTheConfiguredClusterDirectory requires the fdbcli
// one-shot to mount /etc/foundationdb and read /etc/foundationdb/fdb.cluster
// when the setting is unset, and to mount a configured directory at the same
// path inside the one-shot.
func TestStoreCLIOptionsMountTheConfiguredClusterDirectory(t *testing.T) {
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
			cfg, err := loadStoreTestConfig(t, testCase.directory)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			options := storeCLIOptions(cfg, "status minimal")
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

// TestConfigLoadRefusesARelativeClusterDirectory requires config.Load to
// reject a relative directory, which Docker would read as a named volume.
func TestConfigLoadRefusesARelativeClusterDirectory(t *testing.T) {
	if _, err := loadStoreTestConfig(t, "fdb"); err == nil {
		t.Fatal("config.Load accepted a relative cluster file directory")
	}
}
