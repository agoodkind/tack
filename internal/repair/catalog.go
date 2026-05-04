package repair

import (
	"fmt"
	"strings"

	"goodkind.io/tack/internal/domain"
)

// RepairClass identifies one concrete repair strategy.
type RepairClass string

const (
	// RepairClassStrayAliasState repairs a raw `state` alias into canonical
	// `state_id` storage for one workflow-scoped node.
	RepairClassStrayAliasState RepairClass = "stray_alias_state"
)

// RepairClassInfo describes one supported repair class.
type RepairClassInfo struct {
	Class       RepairClass
	Description string
}

var repairClassCatalog = []RepairClassInfo{{
	Class:       RepairClassStrayAliasState,
	Description: "Remove a raw `state` alias after resolving the canonical `state_id` winner for one workflow-scoped node.",
}}

// RepairClasses returns the supported repair classes.
func RepairClasses() []RepairClassInfo {
	classes := make([]RepairClassInfo, len(repairClassCatalog))
	copy(classes, repairClassCatalog)
	return classes
}

// DefaultRepairClass returns the default repair class for CLI usage.
func DefaultRepairClass() RepairClass {
	return RepairClassStrayAliasState
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
