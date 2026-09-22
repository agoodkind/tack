package integration

import "testing"

func TestSearchControl(t *testing.T) {
	adapter, client := nativeSearchClients(t)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const index = "native-control-verification"
	spec := createNativeSearchIndex(t, adapter, client, model, index)
	if err := adapter.EnsureIndex(t.Context(), index, spec); err != nil {
		t.Fatalf("repeated provision changed an existing index: %v", err)
	}
	replicated := spec
	replicated.Replicas = 1
	if err := adapter.EnsureIndex(t.Context(), index, replicated); err != nil {
		t.Fatalf("provision did not apply the configured replica count: %v", err)
	}
	if err := adapter.EnsureIndex(t.Context(), index, spec); err != nil {
		t.Fatalf("provision did not restore the configured replica count: %v", err)
	}
	if err := adapter.SetAlias(t.Context(), "native-control", index); err != nil {
		t.Fatal(err)
	}
	if err := adapter.VerifyIndex(t.Context(), index, spec); err != nil {
		t.Fatal(err)
	}
	target, err := adapter.AliasTarget(t.Context(), "native-control")
	if err != nil || target != index {
		t.Fatalf("alias target = %q, error = %v", target, err)
	}
	wrong := spec
	wrong.MappingVersion = "unexpected"
	if err := adapter.VerifyIndex(t.Context(), index, wrong); err == nil {
		t.Fatal("verification accepted a different mapping version")
	}
	wrong = spec
	wrong.Primaries = 2
	if err := adapter.VerifyIndex(t.Context(), index, wrong); err == nil {
		t.Fatal("verification accepted a different primary shard count")
	}
}
