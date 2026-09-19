package testenv

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// composeFileName is the stack file whose services pin the live stores'
// images. The test engines read their images from it rather than repeating
// the tags, so a test always runs the version production runs (TACK-478).
const composeFileName = "docker-compose.yml"

// composeFile is the part of the stack file the test engines read.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

// composeService is one service of the stack file.
type composeService struct {
	Image string `yaml:"image"`
}

// defaultedVariable matches the compose form ${NAME:-default}.
var defaultedVariable = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*):-([^}]*)\}`)

// serviceImage returns the image the stack file runs for service, with any
// ${NAME:-default} reference resolved the way compose resolves it.
func serviceImage(ctx context.Context, service string) (string, error) {
	root, err := repoRoot(ctx)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, composeFileName)
	contents, err := os.ReadFile(path)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.compose.read_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var stack composeFile
	if err := yaml.Unmarshal(contents, &stack); err != nil {
		slog.ErrorContext(ctx, "testenv.compose.parse_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	image := stack.Services[service].Image
	if image == "" {
		return "", fmt.Errorf("%s declares no image for service %s", path, service)
	}
	return defaultedVariable.ReplaceAllStringFunc(image, func(reference string) string {
		parts := defaultedVariable.FindStringSubmatch(reference)
		if value := os.Getenv(parts[1]); value != "" {
			return value
		}
		return parts[2]
	}), nil
}

// repoRoot walks up from the working directory, which go test sets to the
// package under test, to the directory holding go.mod.
func repoRoot(ctx context.Context) (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		slog.ErrorContext(ctx, "testenv.workdir_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read the working directory: %w", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("no go.mod above the working directory")
		}
		directory = parent
	}
}

// engineName names the container for one engine, image, and command. The
// digest in the name keeps engines of two different pins or configurations
// apart, so a branch that changes either never reuses or replaces the other's
// engine.
func engineName(engineKind, image string, command []string) string {
	sum := sha256.Sum256([]byte(image + "\x00" + strings.Join(command, "\x00")))
	return "tack-testenv-" + engineKind + "-" + hex.EncodeToString(sum[:4])
}

// randomHex returns size random bytes, hex encoded.
func randomHex(ctx context.Context, size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		slog.ErrorContext(ctx, "testenv.random_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
