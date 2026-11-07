package ops

import (
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

var (
	_ clispec.ReadResult = (*inspectReadResult)(nil)
	_ clispec.ReadResult = (*inspectListResult)(nil)
	_ clispec.ReadResult = (*repairVerifyResult)(nil)
	_ clispec.ReadResult = (*repairValidateResult)(nil)
	_ clispec.ReadResult = (*previewOutput)(nil)
	_ clispec.ReadResult = (*authTokenListResult)(nil)
)

func (r inspectReadResult) AuditReadTargets() ([]audit.Target, error) {
	return []audit.Target{nodeResourceTarget(r.NodeID.String())}, nil
}

func (r inspectListResult) AuditReadTargets() ([]audit.Target, error) {
	targets := make([]audit.Target, 0, len(r.Views))
	for _, view := range r.Views {
		if view != nil {
			targets = append(targets, nodeResourceTarget(view.ID.String()))
		}
	}
	return targets, nil
}

func (r repairVerifyResult) AuditReadTargets() ([]audit.Target, error) {
	return []audit.Target{nodeResourceTarget(r.NodeID.String())}, nil
}

func (r repairValidateResult) AuditReadTargets() ([]audit.Target, error) {
	return []audit.Target{nodeResourceTarget(r.NodeID)}, nil
}

func (r previewOutput) AuditReadTargets() ([]audit.Target, error) {
	if r.Preview == nil {
		return []audit.Target{}, nil
	}
	return []audit.Target{nodeResourceTarget(r.Preview.NodeID.String())}, nil
}

func (r authTokenListResult) AuditReadTargets() ([]audit.Target, error) {
	targets := make([]audit.Target, 0, len(r.Tokens))
	for _, token := range r.Tokens {
		targets = append(targets, audit.ResourceTarget(audit.ResourceURI(audit.AuthRecordURIPrefix, token.TokenID)))
	}
	return targets, nil
}
