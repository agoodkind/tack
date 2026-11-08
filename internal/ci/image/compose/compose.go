// Package compose reads pinned image references from the Compose stack file.
package compose

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// YugabyteImagePrefix is the repository that the pinned Yugabyte image must use.
const YugabyteImagePrefix = "yugabytedb/yugabyte:"

type stack struct {
	YugabyteImage string `yaml:"x-yugabyte-image"`
}

// YugabyteImage returns the literal x-yugabyte-image value from the Compose file at path.
func YugabyteImage(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", &os.PathError{Op: "read Compose file", Path: path, Err: err}
	}
	var parsed stack
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		return "", fmt.Errorf("parse Compose file %s: %w", path, err)
	}
	if !strings.HasPrefix(parsed.YugabyteImage, YugabyteImagePrefix) ||
		len(parsed.YugabyteImage) == len(YugabyteImagePrefix) ||
		strings.ContainsAny(parsed.YugabyteImage, " \t\r\n") {
		return "", fmt.Errorf("invalid or missing x-yugabyte-image value in %s", path)
	}
	return parsed.YugabyteImage, nil
}
