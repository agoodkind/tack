package audit

import (
	"strings"

	"github.com/google/uuid"
)

const (
	// EventURIPrefix starts the resource URI of one audit event.
	EventURIPrefix = "tack:audit-event/"
	// AuditOrgURIPrefix starts the resource URI of the ledger rows of one org.
	AuditOrgURIPrefix = "tack:audit-org/"
	// NodeURIPrefix starts the resource URI of one node.
	NodeURIPrefix = "tack:node/"
	// OrgURIPrefix starts the resource URI of one org.
	OrgURIPrefix = "tack:org/"
	// AuthRecordURIPrefix starts the resource URI of one API token record.
	AuthRecordURIPrefix = "tack:api-token/"
	// InvalidURISegment replaces an identifier that is not a UUID.
	InvalidURISegment = "invalid"
)

// ResourceTarget returns the resource target for uri.
func ResourceTarget(uri string) Target {
	return Target{
		Kind: TargetKindResource, NodeType: "", Reference: "", Name: "", URI: uri,
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// CanonicalUUID returns the canonical form of value, or an empty string when value is not a UUID.
func CanonicalUUID(value string) string {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return id.String()
}

// ResourceURI returns prefix followed by the canonical form of value, or by InvalidURISegment when value is not a UUID.
func ResourceURI(prefix, value string) string {
	id := CanonicalUUID(value)
	if id == "" {
		return prefix + InvalidURISegment
	}
	return prefix + id
}
