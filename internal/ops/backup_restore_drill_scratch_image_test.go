// Scratch tests read engine images from docker-compose.yml, matching the
// versions that internal/testenv uses for live-store tests.

package ops

import (
	"os"
	"regexp"
	"testing"

	"go.yaml.in/yaml/v3"
)

// composeDefaultedVariable matches the Compose form ${NAME:-default}.
var composeDefaultedVariable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*):-([^}]*)\}`)

// composeServiceImage reads a service image and resolves ${NAME:-default}
// references using Compose's nonempty-environment-value rule.
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
		t.Fatalf("%s does not specify an image for service %s", path, service)
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
