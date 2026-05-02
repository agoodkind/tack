package node

// DefaultReferenceScope declares where a reference-valued property default is resolved.
type DefaultReferenceScope string

const (
	// DefaultReferenceScopeCreateScope resolves the default beneath the scope used
	// for the node creation request.
	DefaultReferenceScopeCreateScope DefaultReferenceScope = "create_scope"
)

// PropertyDefaultReference declares a human-reference default for a UUID-valued
// property. Reference is interpreted through the target NodeType Reference
// contract declared by PropertyDef.ReferenceTargetTypeKey.
type PropertyDefaultReference struct {
	// Scope controls the node scope where Reference is resolved.
	Scope DefaultReferenceScope `json:"scope"`
	// Reference is the human-readable reference or local value to resolve.
	Reference string `json:"reference"`
}
