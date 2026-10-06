package integration_test

import (
	"context"
	"testing"
	"time"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/testenv"
)

func TestSearchReplacePinnedModel(t *testing.T) {
	engine := testenv.OpenSearch(t)
	pass := engine.Password
	adapter, err := search.New(t.Context(), search.Config{
		Endpoint: engine.Endpoint, CA: engine.CA, Username: engine.Username,
		Password: pass, RequestTimeout: time.Minute, MaxRetries: 0,
	})
	if err != nil {
		t.Fatalf("create the OpenSearch adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.WithoutCancel(t.Context())) })
	first, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("provision the pinned model: %v", err)
	}

	previous, replaced, err := adapter.ReplacePinnedModel(t.Context())
	if err != nil {
		t.Fatalf("replace the pinned model: %v", err)
	}
	if previous != first.ID {
		t.Fatalf("replaced model ID = %q, want the provisioned model %q", previous, first.ID)
	}
	if replaced.ID == "" || replaced.ID == first.ID {
		t.Fatalf("new model ID = %q, want an ID other than %q", replaced.ID, first.ID)
	}
	if err := adapter.VerifyModel(t.Context(), replaced.ID); err != nil {
		t.Fatalf("verify the new model %s: %v", replaced.ID, err)
	}
	if err := adapter.VerifyModel(t.Context(), first.ID); err == nil {
		t.Fatalf("the deleted model %s still verifies", first.ID)
	}
	reused, err := adapter.Provision(t.Context())
	if err != nil || reused.ID != replaced.ID {
		t.Fatalf("provision after replacement = %q, %v; want the new model %q", reused.ID, err, replaced.ID)
	}
}
