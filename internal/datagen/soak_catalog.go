package datagen

import "slices"

type soakNode struct {
	Reference string
	RawID     string
	Name      string
	Group     stateGroup
	SortOrder int
}

type stateGroup string

const (
	stateGroupBacklog   stateGroup = "backlog"
	stateGroupUnstarted stateGroup = "unstarted"
	stateGroupStarted   stateGroup = "started"
	stateGroupCompleted stateGroup = "completed"
	stateGroupCancelled stateGroup = "cancelled"
)

type soakIssue struct {
	soakNode
	Stage  int
	Reopen bool
}

type soakProject struct {
	Workspace           WorkspaceIdentity
	Reference           string
	RawID               string
	States              []soakNode
	Labels              []soakNode
	Issues              []*soakIssue
	workflowIssueCursor int
	Epics               []soakNode
	Cycles              []soakNode
	Modules             []soakNode
}

func (p *soakProject) workflowStates() []soakNode {
	states := make([]soakNode, 0, len(p.States))
	for _, state := range p.States {
		if state.Group != stateGroupCancelled {
			states = append(states, state)
		}
	}
	slices.SortFunc(states, func(left, right soakNode) int {
		leftRank := workflowGroupRank(left.Group)
		rightRank := workflowGroupRank(right.Group)
		if leftRank != rightRank {
			return leftRank - rightRank
		}
		if left.SortOrder != right.SortOrder {
			return left.SortOrder - right.SortOrder
		}
		return compareStrings(left.RawID, right.RawID)
	})
	return states
}

func workflowGroupRank(group stateGroup) int {
	switch group {
	case stateGroupBacklog, stateGroupUnstarted:
		return 0
	case stateGroupCompleted:
		return 2
	default:
		return 1
	}
}

func compareStrings(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
