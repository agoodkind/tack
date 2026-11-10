// The scratch tests read engine images from the Compose stack, matching the
// image source that internal/testenv uses. The tests use those configured
// versions rather than proving the image revision of a running store.

package ops

import (
	"os"
	"regexp"
	"testing"

	"go.yaml.in/yaml/v3"
)

// composeDefaultedVariable matches the compose form ${NAME:-default}.
var composeDefaultedVariable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*):-([^}]*)\}`)

// composeServiceImage returns the configured image for service and resolves
// supported environment substitutions. A nonempty variable overrides its
// default; an empty or unset variable uses the default.
//
// The supported form matches Compose's default-value syntax:
//
//	${NAME:-default}
func composeServiceImage(t *testing.T, service string) string {
	t.Helper()
	path := repoFilePath(t, "docker-compose.yml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var stack struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(contents, &stack); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	image := stack.Services[service].Image
	if image == "" {
		t.Fatalf("%s declares no image for service %s", path, service)
	}

	return composeDefaultedVariable.ReplaceAllStringFunc(
		image,
		func(reference string) string {
			parts := composeDefaultedVariable.FindStringSubmatch(reference)
			if value := os.Getenv(parts[1]); value != "" {
				return value
			}

			return parts[2]
		},
	)
}
