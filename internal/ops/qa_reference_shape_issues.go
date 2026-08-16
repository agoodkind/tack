package ops

import (
	"fmt"
	"strconv"
)

// referenceShapeIssues lists every issue the shape writes: the nodes in each
// collision first, then plain issues until the corpus is the size the repair
// keyed. Production held a scope's sequences densely, so the plain issues fill
// each scope from its lowest free sequence upward.
func referenceShapeIssues(
	groups []referenceShapeGroup,
	projects []referenceShapeProject,
) ([]referenceShapeIssue, error) {
	used := make(map[string]map[int]bool, len(projects))
	issues := make([]referenceShapeIssue, 0, referenceShapeIssueTarget)
	for _, group := range groups {
		issues = append(issues, referenceShapeIssue{
			ID:      referenceShapeNodeID("keeper", group.Reference),
			Project: group.Project, Sequence: group.Sequence, Colliding: false,
		})
		for _, renamed := range group.Renamed {
			issues = append(issues, referenceShapeIssue{
				ID: renamed, Project: group.Project, Sequence: group.Sequence, Colliding: true,
			})
		}
		markReferenceShapeSequence(used, group.Project, group.Sequence)
	}
	fillers, err := referenceShapeFillers(projects, used, referenceShapeIssueTarget-len(issues))
	if err != nil {
		return nil, err
	}
	return append(issues, fillers...), nil
}

// referenceShapeFillers picks the sequences the plain issues carry. Each scope
// receives its highest sequence first, because the repair seeds that scope's
// counter from it and the seeded value decides every replacement reference the
// repair hands out. The rest of the budget fills free sequences from the
// lowest up, scope by scope.
func referenceShapeFillers(
	projects []referenceShapeProject,
	used map[string]map[int]bool,
	budget int,
) ([]referenceShapeIssue, error) {
	if budget < len(projects) {
		return nil, fmt.Errorf(
			"the collisions leave room for %d more issues, and %d scopes each need one",
			budget, len(projects))
	}
	fillers := make([]referenceShapeIssue, 0, budget)
	for _, project := range projects {
		if used[project.Identifier][project.HighWater] {
			continue
		}
		fillers = append(fillers, referenceShapeFiller(project, project.HighWater))
		markReferenceShapeSequence(used, project.Identifier, project.HighWater)
	}
	remaining := budget - len(fillers)
	for _, project := range projects {
		for sequence := 1; sequence <= project.HighWater && remaining > 0; sequence++ {
			if used[project.Identifier][sequence] {
				continue
			}
			fillers = append(fillers, referenceShapeFiller(project, sequence))
			markReferenceShapeSequence(used, project.Identifier, sequence)
			remaining--
		}
	}
	if remaining > 0 {
		return nil, fmt.Errorf(
			"the scopes hold %d fewer sequences than the corpus needs", remaining)
	}
	return fillers, nil
}

func referenceShapeFiller(project referenceShapeProject, sequence int) referenceShapeIssue {
	key := project.Identifier + "-" + strconv.Itoa(sequence)
	return referenceShapeIssue{
		ID:      referenceShapeNodeID("issue", key),
		Project: project.Identifier, Sequence: sequence, Colliding: false,
	}
}

func markReferenceShapeSequence(used map[string]map[int]bool, project string, sequence int) {
	if used[project] == nil {
		used[project] = make(map[int]bool)
	}
	used[project][sequence] = true
}
