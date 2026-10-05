package config_test

import (
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

func TestLoadSearchWorkerSettingsBoundsTheOperationTimeout(t *testing.T) {
	for _, tc := range []struct {
		timeout, lease string
		accepted       bool
	}{
		{timeout: "2m", lease: "3m", accepted: true},
		{timeout: "121s", lease: "4m", accepted: false},
	} {
		t.Run(tc.timeout, func(t *testing.T) {
			t.Setenv("OPENSEARCH_WORKER_OPERATION_TIMEOUT", tc.timeout)
			t.Setenv("OPENSEARCH_WORKER_LEASE", tc.lease)
			settings, err := config.LoadSearchWorkerSettings(t.Context())
			if tc.accepted && (err != nil || settings.OperationTimeout != 2*time.Minute) {
				t.Fatalf("operation timeout %s with lease %s = %v, %v; want accepted", tc.timeout, tc.lease, settings.OperationTimeout, err)
			}
			if !tc.accepted && err == nil {
				t.Fatalf("operation timeout %s with lease %s was accepted", tc.timeout, tc.lease)
			}
		})
	}
}
