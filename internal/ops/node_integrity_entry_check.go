package ops

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// entryProblem returns the first validation error for entry, or an empty
// string if entry is valid.
func entryProblem(entry NodeIntegrityEntry) string {
	if strings.TrimSpace(entry.NodeType) == "" {
		return "node_type is required"
	}
	if len(entry.Classes) == 0 {
		return "classes must list at least one observed class"
	}
	if problem := evidenceProblem(entry.Evidence); problem != "" {
		return problem
	}
	switch entry.Status {
	case RepairEntryUnresolved:
		if strings.TrimSpace(entry.Reason) == "" {
			return "an unresolved entry requires a reason"
		}
		if entryChanges(entry) {
			return "an unresolved entry must not declare a change"
		}
		return ""
	case RepairEntryExecutable:
		return executableProblem(entry)
	default:
		return fmt.Sprintf("status %q is not executable or unresolved", entry.Status)
	}
}

func evidenceProblem(evidence []RepairEvidence) string {
	if len(evidence) == 0 {
		return "evidence must list at least one reference"
	}
	for _, item := range evidence {
		switch item.Kind {
		case RepairEvidenceObserved, RepairEvidenceInferred, RepairEvidenceDecision:
		default:
			return fmt.Sprintf("evidence kind %q is not observed, inferred, or decision", item.Kind)
		}
		if strings.TrimSpace(item.Reference) == "" {
			return "every evidence item requires a reference"
		}
	}
	return ""
}

func executableProblem(entry NodeIntegrityEntry) string {
	if !entryChanges(entry) {
		return "an executable entry must declare a change"
	}
	if entry.Delete != nil {
		if strings.TrimSpace(entry.Delete.ExportFile) == "" {
			return "a delete requires export_file"
		}
		if entry.Report != nil || resultChanges(entry.Result) {
			return "a delete must not declare another change"
		}
		return ""
	}
	if entry.Report != nil {
		if problem := reportProblem(entry); problem != "" {
			return problem
		}
	}
	for name := range entry.Result.Props {
		if strings.TrimSpace(name) == "" {
			return "a property name is empty"
		}
	}
	return edgesProblem(entry.Result)
}

func reportProblem(entry NodeIntegrityEntry) string {
	report := entry.Report
	if strings.TrimSpace(report.SourceProperty) == "" || strings.TrimSpace(report.NodeType) == "" {
		return "a report requires source_property and node_type"
	}
	if len(report.Props) == 0 {
		return "a report requires the props of the report node"
	}
	removal, declared := entry.Result.Props[report.SourceProperty]
	if !declared || string(removal) != "null" {
		return fmt.Sprintf("the result must remove report property %q", report.SourceProperty)
	}
	return ""
}

func edgesProblem(result RepairResult) string {
	removed := make(map[RepairEdge]bool, len(result.RemoveEdges))
	for _, edge := range result.RemoveEdges {
		if problem := edgeProblem(edge, removed); problem != "" {
			return problem
		}
		removed[edge] = true
	}
	added := make(map[RepairEdge]bool, len(result.AddEdges))
	for _, edge := range result.AddEdges {
		if problem := edgeProblem(edge, added); problem != "" {
			return problem
		}
		if removed[edge] {
			return fmt.Sprintf("edge %s to %s is both added and removed", edge.RelationType, edge.TargetID)
		}
		added[edge] = true
	}
	return ""
}

func edgeProblem(edge RepairEdge, seen map[RepairEdge]bool) string {
	if strings.TrimSpace(edge.RelationType) == "" || edge.TargetID == uuid.Nil {
		return "every edge requires relation_type and target_id"
	}
	if seen[edge] {
		return fmt.Sprintf("edge %s to %s repeats", edge.RelationType, edge.TargetID)
	}
	return ""
}

func entryChanges(entry NodeIntegrityEntry) bool {
	return entry.Report != nil || entry.Delete != nil || resultChanges(entry.Result)
}

func resultChanges(result RepairResult) bool {
	return result.Name != nil || len(result.Props) > 0 || len(result.AddEdges) > 0 || len(result.RemoveEdges) > 0
}
