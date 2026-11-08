// Package compose reads pinned image references from the Compose stack file.
package compose

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// YugabyteImagePrefix is the repository that the pinned Yugabyte image must use.
const YugabyteImagePrefix = "yugabytedb/yugabyte:"

type stack struct {
	YugabyteImage string `yaml:"x-yugabyte-image"`
}

// YugabyteImage returns the x-yugabyte-image value from a Compose file.
//
// The value must start with yugabytedb/yugabyte: and contain a nonempty
// tag without whitespace. YugabyteImage returns an error if the file
// cannot be read or parsed, or if the value is invalid.
//
// For example, this Compose line returns yugabytedb/yugabyte:2024.2.8.0-b85:
//
//	x-yugabyte-image: yugabytedb/yugabyte:2024.2.8.0-b85
func YugabyteImage(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", &os.PathError{Op: "read Compose file", Path: path, Err: err}
	}
	var parsed stack
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		slog.Error("compose.parse_failed", slog.String("path", path), slog.Any("error", err))
		return "", fmt.Errorf("parse Compose file %s: %w", path, err)
	}
	if !strings.HasPrefix(parsed.YugabyteImage, YugabyteImagePrefix) ||
		len(parsed.YugabyteImage) == len(YugabyteImagePrefix) ||
		strings.ContainsAny(parsed.YugabyteImage, " \t\r\n") {
		return "", fmt.Errorf("invalid or missing x-yugabyte-image value in %s", path)
	}
	return parsed.YugabyteImage, nil
}
