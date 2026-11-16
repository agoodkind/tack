package servercli

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
	return clispec.InputReadSpec(
		audit.VerbAuditRead,
		func(in auditQueryInput) audit.Target {
			return audit.ResourceTarget(audit.ResourceURI(audit.AuditOrgURIPrefix, in.Org))
		},
		func(input auditQueryInput) auditQueryInput {
			return auditQueryInput{
				InputMarker: clispec.InputMarker{},
				Org:         audit.CanonicalUUID(input.Org),
				Oldest:      canonicalAuditTime(input.Oldest),
				Latest:      canonicalAuditTime(input.Latest),
				Action:      input.Action,
				ActorID:     audit.CanonicalUUID(input.ActorID),
				EntityID:    audit.CanonicalUUID(input.EntityID),
				RequestID:   input.RequestID,
				TraceID:     input.TraceID,
				Limit:       input.Limit,
				Cursor:      "",
			}
		},
	)
}

func auditGetSpec() audit.Spec {
	return clispec.InputReadSpec(
		audit.VerbAuditRead,
		func(in auditGetInput) audit.Target {
			return audit.ResourceTarget(audit.ResourceURI(audit.EventURIPrefix, in.EventID))
		},
		func(in auditGetInput) auditGetInput {
			return auditGetInput{
				InputMarker: clispec.InputMarker{},
				EventID:     audit.CanonicalUUID(in.EventID),
			}
		},
	)
}

func auditLastChangeSpec() audit.Spec {
	return clispec.InputReadSpec(
		audit.VerbAuditRead,
		func(in auditLastChangeInput) audit.Target {
			return audit.ResourceTarget(audit.ResourceURI(audit.NodeURIPrefix, in.EntityID))
		},
		func(input auditLastChangeInput) auditLastChangeInput {
			return auditLastChangeInput{
				InputMarker: clispec.InputMarker{},
				Org:         audit.CanonicalUUID(input.Org),
				EntityID:    audit.CanonicalUUID(input.EntityID),
				Oldest:      canonicalAuditTime(input.Oldest),
				Latest:      canonicalAuditTime(input.Latest),
				Path:        input.Path,
				Cursor:      "",
			}
		},
	)
}
