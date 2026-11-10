package node

import "strings"

// FeatureHasAddress marks types with a declared human-readable address
// property.
const FeatureHasAddress = "has_address"

// ReferenceDirectProperty resolves nodes with the property that
// ReferenceConfig.Property selects.
const ReferenceDirectProperty ReferenceStrategy = "direct_property"

// IsDirectAddress reports whether the reference uses a direct property
// address.
//
// The check compares Strategy with ReferenceDirectProperty.
//
//   - Other strategies return false even when Property contains text.
func (r ReferenceConfig) IsDirectAddress() bool {
	return r.Strategy == ReferenceDirectProperty
}

// DirectAddressProperty returns the property to include in the address index.
//
// A direct property strategy returns Property after trimming surrounding
// space. Other strategies return an empty string.
//
//   - A blank Property also returns an empty string for a direct strategy.
func (r ReferenceConfig) DirectAddressProperty() string {
	if !r.IsDirectAddress() {
		return ""
	}

	return strings.TrimSpace(r.Property)
}
