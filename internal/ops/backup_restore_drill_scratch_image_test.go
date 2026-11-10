// backup_restore_drill_scratch_image_test.go reads the engine images the
// scratch tests boot from docker-compose.yml, so they run the versions the
// live stores run, the same source internal/testenv reads its engines from.

package ops

import (
	"os"
	"regexp"
	"testing"

	"go.yaml.in/yaml/v3"
)

// composeDefaultedVariable matches the compose form ${NAME:-default}.
var composeDefaultedVariable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*):-([^}]*)\}`)

// composeServiceImage returns the image docker-compose.yml runs for service,
// with any ${NAME:-default} reference resolved the way compose resolves it.
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
	return composeDefaultedVariable.ReplaceAllStringFunc(image, func(reference string) string {
		parts := composeDefaultedVariable.FindStringSubmatch(reference)
		if value := os.Getenv(parts[1]); value != "" {
			return value
		}
		return parts[2]
	})
}
