package config_test

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

const cursorKeyBytes = 32

// setSearchQueryKeyForTest sets a valid cursor key and removes the cursor key
// file and absolute timeout variables during the test.
func setSearchQueryKeyForTest(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"OPENSEARCH_CURSOR_KEY_FILE",
		"OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT",
	} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}

	t.Setenv(
		"OPENSEARCH_CURSOR_KEY",
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", cursorKeyBytes))),
	)
}

// TestLoadSearchQuerySettingsReadsIdleTimeout checks that
// [config.LoadSearchQuerySettings] reads a 90-second
// OPENSEARCH_SESSION_IDLE_TIMEOUT override.
func TestLoadSearchQuerySettingsReadsIdleTimeout(t *testing.T) {
	setSearchQueryKeyForTest(t)
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "90s")
	settings, err := config.LoadSearchQuerySettings(t.Context())
	if err != nil {
		t.Fatalf("loading the 90s idle timeout override failed: %v", err)
	}

	if settings.IdleTimeout != 90*time.Second {
		t.Fatalf("idle timeout is %s but should be 90s", settings.IdleTimeout)
	}
}

// TestLoadSearchQuerySettingsRejectsIdleTimeout checks that
// [config.LoadSearchQuerySettings] rejects OPENSEARCH_SESSION_IDLE_TIMEOUT
// values that it cannot parse or that are zero, negative, or greater than the
// absolute timeout.
func TestLoadSearchQuerySettingsRejectsIdleTimeout(t *testing.T) {
	for _, value := range []string{"0s", "-1m", "two minutes", "3h"} {
		t.Run(
			value,
			func(t *testing.T) {
				setSearchQueryKeyForTest(t)
				t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", value)
				if _, err := config.LoadSearchQuerySettings(t.Context()); err == nil {
					t.Fatalf("loading idle timeout %q did not return an error", value)
				}
			},
		)
	}
}
