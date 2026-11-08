// Package compose reads pinned image references from the Compose stack
// file.
package compose

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// YugabyteImagePrefix is the repository that the pinned Yugabyte image
// must use.
const YugabyteImagePrefix = "yugabytedb/yugabyte:"

var errInvalidImage = errors.New("invalid or missing x-yugabyte-image value")

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

	err = yaml.Unmarshal(content, &parsed)
	if err != nil {
		return "", fmt.Errorf("parse Compose file %s: %w", path, err)
	}

	image := parsed.YugabyteImage
	if !strings.HasPrefix(image, YugabyteImagePrefix) ||
		len(image) == len(YugabyteImagePrefix) ||
		strings.ContainsAny(image, " \t\r\n") {
		return "", fmt.Errorf("%w in %s", errInvalidImage, path)
	}

	return image, nil
}
