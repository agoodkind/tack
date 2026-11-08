package manifest_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ci/image/environment"
	"goodkind.io/tack/internal/ci/image/manifest"
)

const registryKey = "REGISTRY"

// TestPublishReportsEnvironmentFailureWithoutPathError checks that Publish
// rejects a missing or empty environment variable with an error that contains
// the variable key and the original cause, and that the error is not an
// *os.PathError.
func TestPublishReportsEnvironmentFailureWithoutPathError(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv(registryKey, "")

		err := os.Unsetenv(registryKey)
		if err != nil {
			t.Fatalf("unset %s: %v", registryKey, err)
		}

		err = manifest.Publish(context.Background())
		if !errors.Is(err, environment.ErrUnset) {
			t.Fatalf("err = %v, want cause %v", err, environment.ErrUnset)
		}

		assertEnvironmentError(t, err, "unset")
	})

	t.Run("empty", func(t *testing.T) {
		t.Setenv(registryKey, "")

		err := manifest.Publish(context.Background())
		if err == nil {
			t.Fatal("expected failure for an empty variable")
		}

		assertEnvironmentError(t, err, "empty")
	})
}

func assertEnvironmentError(t *testing.T, err error, wantCause string) {
	t.Helper()

	if !strings.Contains(err.Error(), registryKey) || !strings.Contains(err.Error(), wantCause) {
		t.Fatalf("err = %q, want key %s and cause containing %q", err, registryKey, wantCause)
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		t.Fatalf("err = %v is an *os.PathError", err)
	}
}
