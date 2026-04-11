//go:build fdb

package foundationdb

import (
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// systemPropID generates a deterministic UUID for system property definitions
// using a fixed namespace UUID and the workspace ID + property name as input.
func systemPropID(workspaceID uuid.UUID, name string) uuid.UUID {
	tackPropNamespace := uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")
	return uuid.NewSHA1(tackPropNamespace, []byte(workspaceID.String()+":"+name))
}

// textPV creates a PropertyValue of kind Text with the given string.
func textPV(s string) *node.PropertyValue {
	return &node.PropertyValue{Kind: node.PropertyValueText, Text: &s}
}

// System property names used by multiple stores
const (
	propNameColor            = "color"
	propNameSortOrder        = "sort_order"
	propNameGroupName        = "group_name"
	propNameIdentifier       = "identifier"
	propNameDescription      = "description"
	propNameNetwork          = "network"
	propNameDefaultStateID   = "default_state_id"
)
