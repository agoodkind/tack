package node

// DefaultReferenceScope selects where a reference-valued property default
// resolves.
type DefaultReferenceScope string

const (
	// DefaultReferenceScopeCreateScope resolves the default beneath the scope
	// that the node creation request uses.
	DefaultReferenceScopeCreateScope DefaultReferenceScope = "create_scope"
)

// PropertyDefaultReference specifies a human-readable reference as the default
// for a UUID-valued property. PropertyDef.ReferenceTargetTypeKey selects the
// target NodeType, and its Reference contract interprets Reference.
type PropertyDefaultReference struct {
	// Scope selects the node scope that resolves Reference.
	Scope DefaultReferenceScope `json:"scope"`
	// Reference contains the human-readable reference or local value to resolve.
	Reference string `json:"reference"`
}
