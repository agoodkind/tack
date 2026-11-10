package node

import "testing"

func TestReferenceConfigDirectAddressProperty(t *testing.T) {
	reference := ReferenceConfig{Strategy: ReferenceDirectProperty, Property: " identifier "}

	if !reference.IsDirectAddress() {
		t.Fatal("ReferenceDirectProperty should be a direct address strategy")
	}
	if got := reference.DirectAddressProperty(); got != "identifier" {
		t.Fatalf("DirectAddressProperty() = %q, want identifier", got)
	}
}

func TestReferenceConfigUUIDOnlyHasNoAddressProperty(t *testing.T) {
	reference := ReferenceConfig{Strategy: ReferenceUUIDOnly, Property: "identifier"}

	if reference.IsDirectAddress() {
		t.Fatal("ReferenceUUIDOnly should not be a direct address strategy")
	}
	if got := reference.DirectAddressProperty(); got != "" {
		t.Fatalf("DirectAddressProperty() = %q, want empty", got)
	}
}
