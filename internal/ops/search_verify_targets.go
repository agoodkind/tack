package ops

import (
	"net/url"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// AuditReadTargets identifies each diagnostic record by its storage key in output order.
func (output searchVerifyOutput) AuditReadTargets() ([]audit.Target, error) {
	targets := make([]audit.Target, 0, len(output.Exclusions)+len(output.Warnings)+len(output.StuckWork))
	for _, exclusion := range output.Exclusions {
		targets = append(targets, searchDiagnosticTarget("exclusions/"+exclusion.OrgID+"/"+exclusion.NodeID, ""))
	}
	for _, warning := range output.Warnings {
		targets = append(targets, searchDiagnosticTarget("warnings/"+warning.OrgID+"/"+warning.NodeID+"/"+url.PathEscape(warning.PropertyName), ""))
	}
	for _, work := range output.StuckWork {
		targets = append(targets, searchDiagnosticTarget("work/"+url.PathEscape(work.Kind)+"/"+work.orgID.String()+"/"+work.nodeID.String(), work.ItemID))
	}
	return targets, nil
}

func searchDiagnosticTarget(key, reference string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: reference, Name: "", URI: "tack:search/" + key,
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}
