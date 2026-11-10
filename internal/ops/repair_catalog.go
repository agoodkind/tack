package ops

import (
	"fmt"
	"strings"

	"goodkind.io/tack/internal/domain"
)

// RepairClassInfo describes one supported repair class.
type RepairClassInfo struct {
	Class       RepairClass
	Description string
}

var repairClassCatalog = []RepairClassInfo{
	{
		Class:       RepairClassReferenceProperty,
		Description: "Repair one UUID reference property from operator-declared source fields and policies.",
	},
	{
		Class:       RepairClassParentReference,
		Description: "Repair a node parent_id and child_of edge from operator-declared source fields.",
	},
	{
		Class:       RepairClassPropsTransform,
		Description: "Apply generic property delete, rename, and append-preserve transforms.",
	},
}

// RepairClasses returns the supported repair classes.
func RepairClasses() []RepairClassInfo {
	classes := make([]RepairClassInfo, len(repairClassCatalog))
	copy(classes, repairClassCatalog)
	return classes
}

// DefaultRepairClass returns the default repair class for CLI usage.
func DefaultRepairClass() RepairClass {
	return RepairClassReferenceProperty
}

// ParseRepairClass validates one repair class input.
func ParseRepairClass(rawValue string) (RepairClass, error) {
	trimmedValue := strings.TrimSpace(rawValue)
	if trimmedValue == "" {
		return "", fmt.Errorf("repair class is required: %w", domain.ErrInvalidArgument)
	}
	repairClass := RepairClass(trimmedValue)
	for _, info := range repairClassCatalog {
		if info.Class == repairClass {
			return repairClass, nil
		}
	}
	return "", fmt.Errorf("repair class %q: %w", trimmedValue, domain.ErrInvalidArgument)
}
