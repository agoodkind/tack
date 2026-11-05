//go:build darwin && integration

package testenv_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerComposeIdentities(t *testing.T) {
	for _, identity := range []string{"directory", "file", "environment", "explicit resources"} {
		t.Run(identity, func(t *testing.T) {
			options, _ := macFixture(t)
			compose := macRead(t, options.Root, "docker-compose.test.yml")
			expected := filepath.Base(options.Root)
			switch identity {
			case "file":
				compose += "\nname: file-project\n"
				expected = "file-project"
			case "environment":
				compose += "\nname: file-project\n"
				t.Setenv("COMPOSE_PROJECT_NAME", "environment-project")
				expected = "environment-project"
			case "explicit resources":
				compose = strings.Replace(compose, "  tack-test-go-cache:\n", "  tack-test-go-cache:\n    name: explicit-build-cache\n", 1)
				compose += "\nnetworks:\n  default:\n    name: explicit-network\n"
			}
			macWrite(t, options.Root, "docker-compose.test.yml", compose)
			macWrite(t, options.Root, ".env", "COMPOSE_PROJECT_NAME=unrelated-project\nTACK_SEARCH_CLUSTER=unrelated-value\n")
			invocation := macPrepare(t, options)
			if invocation.Project != expected {
				t.Fatalf("project precedence selected %s instead of %s", invocation.Project, expected)
			}
			expectedNetwork := expected + "_default"
			expectedCache := expected + "_tack-test-go-cache"
			if identity == "explicit resources" {
				expectedNetwork, expectedCache = "explicit-network", "explicit-build-cache"
			}
			if invocation.Networks["default"].Name != expectedNetwork {
				t.Fatalf("unexpected network identity: %+v", invocation.Networks)
			}
			for _, volume := range invocation.Service.Volumes {
				if volume.Target == "/root/.cache/go-build" && volume.Source != expectedCache {
					t.Fatalf("unexpected build cache identity: %+v", volume)
				}
			}
			if *invocation.Service.Environment["TACK_SEARCH_CLUSTER"] != "1" {
				t.Fatal("Compose read TACK_SEARCH_CLUSTER from .env")
			}
		})
	}
}

func TestMacRunnerRejectsUnsupportedCompose(t *testing.T) {
	for _, field := range []string{"env_file", "privileged", "entrypoint", "extra volume"} {
		t.Run(field, func(t *testing.T) {
			options, _ := macFixture(t)
			compose := macRead(t, options.Root, "docker-compose.test.yml")
			addition := "    " + field + ": true\n"
			switch field {
			case "env_file":
				addition = "    env_file: /missing/environment\n"
			case "entrypoint":
				addition = "    entrypoint: [unexpected]\n"
			case "extra volume":
				addition = ""
				compose = strings.Replace(compose, "    volumes:\n", "    volumes:\n      - /unexpected:/unexpected\n", 1)
			}
			compose = strings.Replace(compose, "  tests:\n", "  tests:\n"+addition, 1)
			macWrite(t, options.Root, "docker-compose.test.yml", compose)
			if _, err := testenv.RunMacTests(t.Context(), options); err == nil || strings.Contains(err.Error(), "inspect required immutable local image") {
				t.Fatalf("RunMacTests accepted unsupported Compose %s: %v", field, err)
			}
			if _, err := os.Lstat(options.EvidenceDirectory); !os.IsNotExist(err) {
				t.Fatalf("unsupported Compose created evidence: %v", err)
			}
		})
	}
}
