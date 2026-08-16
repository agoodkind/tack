package ops

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain/node"
)

// deriveReferenceShape builds the pre-repair corpus from the same rename
// evidence the ledger reconstruction reads, so the corpus and the
// reconstruction always describe one repair rather than two guesses at it.
func deriveReferenceShape(renames []referenceRenameEvidence) (referenceShape, error) {
	groups, highWater, err := referenceShapeGroups(renames)
	if err != nil {
		return referenceShape{}, err
	}
	projects, err := referenceShapeProjects(highWater)
	if err != nil {
		return referenceShape{}, err
	}
	issues, err := referenceShapeIssues(groups, projects)
	if err != nil {
		return referenceShape{}, err
	}
	renamed := 0
	for _, group := range groups {
		renamed += len(group.Renamed)
	}
	orgID := uuid.NewSHA1(referenceShapeNamespace, []byte(productionSeedOrgSlug))
	shape := referenceShape{
		OrgID:       orgID,
		WorkspaceID: node.WorkspaceID(orgID, productionSeedWorkspaceSlug),
		Projects:    projects,
		Issues:      issues,
		Groups:      groups,
		Renames:     renamed,
	}
	return shape, validateReferenceShape(shape)
}

// referenceShapeGroups turns the renames the repair performed on its first day
// into the collisions that produced them, plus the sequence each scope has to
// reach for the repair to hand out the references the evidence records.
func referenceShapeGroups(
	renames []referenceRenameEvidence,
) ([]referenceShapeGroup, map[string]int, error) {
	grouped := make(map[string]*referenceShapeGroup)
	highWater := make(map[string]int)
	for _, rename := range renames {
		if historicalReferenceRenameTime(rename) != referenceRepairDate {
			continue
		}
		group, err := referenceShapeGroupOf(grouped, rename)
		if err != nil {
			return nil, nil, err
		}
		nodeID, err := uuid.Parse(rename.NodeID)
		if err != nil {
			return nil, nil, fmt.Errorf("rename evidence node id %q is not a UUID", rename.NodeID)
		}
		group.Renamed = append(group.Renamed, nodeID)
		project, allocated, err := splitScopedReference(rename.NewReference)
		if err != nil {
			return nil, nil, err
		}
		if current, seen := highWater[project]; !seen || allocated <= current {
			highWater[project] = allocated - 1
		}
	}
	return sortedReferenceShapeGroups(grouped), highWater, nil
}

func referenceShapeGroupOf(
	grouped map[string]*referenceShapeGroup,
	rename referenceRenameEvidence,
) (*referenceShapeGroup, error) {
	if group, seen := grouped[rename.OldReference]; seen {
		return group, nil
	}
	project, sequence, err := splitScopedReference(rename.OldReference)
	if err != nil {
		return nil, err
	}
	group := &referenceShapeGroup{
		Reference: rename.OldReference, Project: project, Sequence: sequence, Renamed: nil,
	}
	grouped[rename.OldReference] = group
	return group, nil
}

// sortedReferenceShapeGroups orders the groups, and the nodes inside each one,
// the way the repair walks them: collisions by rendered reference, and the
// nodes in one collision by identifier, oldest first. The order decides which
// renamed node receives which replacement reference.
func sortedReferenceShapeGroups(grouped map[string]*referenceShapeGroup) []referenceShapeGroup {
	groups := make([]referenceShapeGroup, 0, len(grouped))
	for _, group := range grouped {
		sort.Slice(group.Renamed, func(i, j int) bool {
			return group.Renamed[i].String() < group.Renamed[j].String()
		})
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Reference < groups[j].Reference })
	return groups
}

// referenceShapeProjects lists every scope the shape writes: the ones the
// evidence names, then quiet ones until the scope count matches what the
// repair recorded.
func referenceShapeProjects(highWater map[string]int) ([]referenceShapeProject, error) {
	identifiers := make([]string, 0, len(highWater))
	for identifier := range highWater {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)
	projects := make([]referenceShapeProject, 0, recordedCounterSeeds)
	for _, identifier := range identifiers {
		projects = append(projects, referenceShapeProject{
			Identifier: identifier,
			ID:         referenceShapeNodeID("project", identifier),
			HighWater:  highWater[identifier],
		})
	}
	quiet := recordedCounterSeeds - len(projects)
	if quiet < 0 {
		return nil, fmt.Errorf(
			"rename evidence names %d scopes, more than the %d the repair recorded",
			len(projects), recordedCounterSeeds)
	}
	if quiet > len(referenceShapeQuietProjects) {
		return nil, fmt.Errorf(
			"the shape needs %d quiet scopes and only %d are named",
			quiet, len(referenceShapeQuietProjects))
	}
	for _, identifier := range referenceShapeQuietProjects[:quiet] {
		projects = append(projects, referenceShapeProject{
			Identifier: identifier,
			ID:         referenceShapeNodeID("project", identifier),
			HighWater:  referenceShapeQuietHighWater,
		})
	}
	return projects, nil
}

func splitScopedReference(reference string) (string, int, error) {
	separator := strings.LastIndex(reference, "-")
	if separator <= 0 || separator == len(reference)-1 {
		return "", 0, fmt.Errorf("reference %q is not a scope and a sequence", reference)
	}
	sequence, err := strconv.Atoi(reference[separator+1:])
	if err != nil || sequence <= 0 {
		return "", 0, fmt.Errorf("reference %q carries no positive sequence", reference)
	}
	return reference[:separator], sequence, nil
}

func validateReferenceShape(shape referenceShape) error {
	if len(shape.Issues) != referenceShapeIssueTarget {
		return fmt.Errorf("shape holds %d issues, want %d",
			len(shape.Issues), referenceShapeIssueTarget)
	}
	if len(shape.Projects) != recordedCounterSeeds {
		return fmt.Errorf("shape holds %d scopes, want %d",
			len(shape.Projects), recordedCounterSeeds)
	}
	if shape.Renames != recordedReferenceRenames-recordedFollowupReferenceKey {
		return fmt.Errorf("shape renames %d issues, want %d",
			shape.Renames, recordedReferenceRenames-recordedFollowupReferenceKey)
	}
	return validateReferenceShapeScopes(shape)
}

// validateReferenceShapeScopes checks the two properties the repair reads from
// the corpus: every scope holds issues, so the scope count the reconstruction
// derives is right, and each scope's highest sequence is the value the repair
// seeds its counter to.
func validateReferenceShapeScopes(shape referenceShape) error {
	highest := make(map[string]int, len(shape.Projects))
	for _, issue := range shape.Issues {
		if issue.Sequence > highest[issue.Project] {
			highest[issue.Project] = issue.Sequence
		}
	}
	if len(highest) != len(shape.Projects) {
		return fmt.Errorf("%d of %d scopes hold issues", len(highest), len(shape.Projects))
	}
	for _, project := range shape.Projects {
		if highest[project.Identifier] != project.HighWater {
			return fmt.Errorf("scope %s tops out at %d, want %d",
				project.Identifier, highest[project.Identifier], project.HighWater)
		}
	}
	if len(shape.Groups) == 0 {
		return errors.New("shape holds no colliding references")
	}
	return nil
}
