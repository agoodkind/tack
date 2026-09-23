package config

import "testing"

func TestLoadSearchWorkerSettingsAcceptsDefaultClassWeights(t *testing.T) {
	unsetForTest(t, "OPENSEARCH_WORK_CLASS_WEIGHTS")
	if _, err := LoadSearchWorkerSettings(t.Context()); err != nil {
		t.Fatalf("LoadSearchWorkerSettings with default class weights: %v", err)
	}
}

func TestLoadSearchWorkerSettingsRejectsIncompleteClassWeights(t *testing.T) {
	for _, weights := range []string{
		"live:1",
		"live:1,access:1,cleanup:1,rescan:1,archive:1",
	} {
		t.Run(weights, func(t *testing.T) {
			t.Setenv("OPENSEARCH_WORK_CLASS_WEIGHTS", weights)
			if _, err := LoadSearchWorkerSettings(t.Context()); err == nil {
				t.Fatalf("LoadSearchWorkerSettings accepted class weights %q", weights)
			}
		})
	}
}
