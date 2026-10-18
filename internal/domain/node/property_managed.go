package node

// managedProperties lists the properties that the node service writes on
// every node type.
var managedProperties = map[string]bool{"parent_id": true, "scope_id": true, "sequence": true}

// IsManagedProperty reports whether the node service writes the property
// name on every node type.
func IsManagedProperty(name string) bool {
	return managedProperties[name]
}
