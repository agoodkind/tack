package node

import "strings"

// FeatureHasAddress marks types whose instances carry a declared human-readable address property.
const FeatureHasAddress = "has_address"

// AddressKind identifies one materialized human-readable lookup namespace for a node type.
type AddressKind string

// AddressKindPrimary is the default address namespace declared by NodeType.Reference.
const AddressKindPrimary AddressKind = "primary"

// ReferenceDirectProperty resolves nodes from the property named by ReferenceConfig.Property.
const ReferenceDirectProperty ReferenceStrategy = "direct_property"

// IsDirectAddress reports whether the reference is a direct property address.
func (r ReferenceConfig) IsDirectAddress() bool {
	return r.Strategy == ReferenceDirectProperty
}

// DirectAddressProperty returns the property that should be materialized into the address index.
func (r ReferenceConfig) DirectAddressProperty() string {
	if !r.IsDirectAddress() {
		return ""
	}
	return strings.TrimSpace(r.Property)
}
