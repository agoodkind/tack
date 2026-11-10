package main

import (
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

func canonicalAuditTime(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return ""
	}
	return parsed.Format(time.RFC3339)
}

func auditQuerySpec() audit.Spec {
	return clispec.InputReadSpec(audit.VerbAuditRead,
		func(in auditQueryInput) audit.Target {
			return audit.ResourceTarget(audit.ResourceURI(audit.AuditOrgURIPrefix, in.Org))
		},
		func(in auditQueryInput) auditQueryInput {
			return auditQueryInput{
				InputMarker: clispec.InputMarker{}, Org: audit.CanonicalUUID(in.Org), Oldest: canonicalAuditTime(in.Oldest),
				Latest: canonicalAuditTime(in.Latest), Action: in.Action, ActorID: audit.CanonicalUUID(in.ActorID),
				EntityID: audit.CanonicalUUID(in.EntityID), RequestID: in.RequestID, TraceID: in.TraceID,
				Limit: in.Limit, Cursor: "",
			}
		})
}

func auditGetSpec() audit.Spec {
	return clispec.InputReadSpec(audit.VerbAuditRead,
		func(in auditGetInput) audit.Target {
			return audit.ResourceTarget(audit.ResourceURI(audit.EventURIPrefix, in.EventID))
		},
		func(in auditGetInput) auditGetInput {
			return auditGetInput{InputMarker: clispec.InputMarker{}, EventID: audit.CanonicalUUID(in.EventID)}
		})
}

func auditLastChangeSpec() audit.Spec {
	return clispec.InputReadSpec(audit.VerbAuditRead,
		func(in auditLastChangeInput) audit.Target {
			return audit.ResourceTarget(audit.ResourceURI(audit.NodeURIPrefix, in.EntityID))
		},
		func(in auditLastChangeInput) auditLastChangeInput {
			return auditLastChangeInput{
				InputMarker: clispec.InputMarker{}, Org: audit.CanonicalUUID(in.Org), EntityID: audit.CanonicalUUID(in.EntityID),
				Oldest: canonicalAuditTime(in.Oldest), Latest: canonicalAuditTime(in.Latest), Path: in.Path, Cursor: "",
			}
		})
}
