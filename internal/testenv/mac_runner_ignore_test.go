package testenv_test

import (
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerGlobalIgnores(t *testing.T) {
	for _, mode := range []string{"default", "configured relative", "explicit empty", "missing configured"} {
		t.Run(mode, func(t *testing.T) {
			options, repository := macFixture(t)
			t.Setenv("GIT_CONFIG_GLOBAL", "")
			t.Setenv("GIT_CONFIG_SYSTEM", "")
			xdg := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", xdg)
			macWrite(t, xdg, "git/ignore", "global-ignored.go\n")
			macWrite(t, options.Root, "global-ignored.go", "package example\n")
			configuration, err := repository.Config()
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "configured relative":
				configuration.Raw.Section("core").SetOption("excludesFile", "ignore-rules")
				macWrite(t, options.Root, "ignore-rules", "global-ignored.go\n")
			case "explicit empty":
				configuration.Raw.Section("core").SetOption("excludesFile", "")
			case "missing configured":
				configuration.Raw.Section("core").SetOption("excludesFile", filepath.Join(options.Root, "missing-ignore-rules"))
			}
			if err := repository.SetConfig(configuration); err != nil {
				t.Fatal(err)
			}
			if mode == "missing configured" {
				if _, err := testenv.RunMacTests(t.Context(), options); err == nil || !strings.Contains(err.Error(), "read Git ignore file") {
					t.Fatalf("RunMacTests accepted missing required ignore metadata: %v", err)
				}
				return
			}
			macPrepare(t, options)
			manifest := macRead(t, options.EvidenceDirectory, "source.sha256")
			included := strings.Contains(manifest, "global-ignored.go")
			if included != (mode == "explicit empty") {
				t.Fatalf("unexpected %s global ignore result: %s", mode, manifest)
			}
		})
	}
}
