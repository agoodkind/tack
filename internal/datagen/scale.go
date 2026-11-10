// Package datagen builds deterministic QA data through Tack's MCP request surface.
package datagen

import "fmt"

type scaleName string

const (
	scaleSmall  scaleName = "small"
	scaleMedium scaleName = "medium"
	scaleLarge  scaleName = "large"
)

// Scale controls the amount of QA data produced.
type Scale struct {
	Name                 string
	OrgCount             int
	WorkspacesPerOrg     int
	ProjectsPerWorkspace int
	IssuesPerProject     int
	CommentsPerIssue     int
	ActivitiesPerIssue   int
	ContainersPerProject int
	LabelsPerWorkspace   int
	AdditionalStateCount int
	ActorsPerOrg         int
}

// ParseScale returns the count plan for a named generator scale.
func ParseScale(name string) (Scale, error) {
	switch scaleName(name) {
	case scaleSmall:
		return Scale{
			Name: "small", OrgCount: 1, WorkspacesPerOrg: 1,
			ProjectsPerWorkspace: 1, IssuesPerProject: 6,
			CommentsPerIssue: 1, ActivitiesPerIssue: 1,
			ContainersPerProject: 1, LabelsPerWorkspace: 4,
			AdditionalStateCount: 2, ActorsPerOrg: 3,
		}, nil
	case scaleMedium:
		return Scale{
			Name: "medium", OrgCount: 2, WorkspacesPerOrg: 2,
			ProjectsPerWorkspace: 2, IssuesPerProject: 16,
			CommentsPerIssue: 2, ActivitiesPerIssue: 2,
			ContainersPerProject: 2, LabelsPerWorkspace: 6,
			AdditionalStateCount: 3, ActorsPerOrg: 5,
		}, nil
	case scaleLarge:
		return Scale{
			Name: "large", OrgCount: 3, WorkspacesPerOrg: 3,
			ProjectsPerWorkspace: 3, IssuesPerProject: 40,
			CommentsPerIssue: 3, ActivitiesPerIssue: 3,
			ContainersPerProject: 3, LabelsPerWorkspace: 8,
			AdditionalStateCount: 4, ActorsPerOrg: 8,
		}, nil
	default:
		return Scale{}, fmt.Errorf("qa datagen: unknown scale %q; use small, medium, or large", name)
	}
}
