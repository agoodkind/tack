package audit

import (
	"strings"

	"github.com/google/uuid"
)

const (
	// EventURIPrefix is the prefix of the resource URI for one audit event.
	EventURIPrefix = "tack:audit-event/"
	// AuditOrgURIPrefix is the prefix of the resource URI for the ledger rows
	// of one organization.
	AuditOrgURIPrefix = "tack:audit-org/"
	// NodeURIPrefix is the prefix of the resource URI for one node.
	NodeURIPrefix = "tack:node/"
	// OrgURIPrefix is the prefix of the resource URI for one organization.
	OrgURIPrefix = "tack:org/"
	// APIAccessURIPrefix placeholder.
	APIAccessURIPrefix = "tack:api_token/"
	// InvalidURISegment is the URI segment that replaces an identifier that is
	// not a UUID.
	InvalidURISegment = "invalid"
)

// ResourceTarget returns the resource target for uri.
func ResourceTarget(uri string) Target {
	return Target{
		Kind: TargetKindResource, NodeType: "", Reference: "", Name: "", URI: uri,
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// NodeTarget placeholder.
func NodeTarget(nodeID uuid.UUID) Target {
	return ResourceTarget(NodeURIPrefix + nodeID.String())
}

// EventTarget placeholder.
func EventTarget(eventID uuid.UUID) Target {
	return ResourceTarget(EventURIPrefix + eventID.String())
}

// CanonicalUUID returns the canonical form of value, or an empty string when
// value is not a UUID.
func CanonicalUUID(value string) string {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return id.String()
}

// ResourceURI returns prefix followed by the canonical form of value. When value
// is not a UUID, it returns prefix followed by InvalidURISegment.
func ResourceURI(prefix, value string) string {
	id := CanonicalUUID(value)
	if id == "" {
		return prefix + InvalidURISegment
	}
	return prefix + id
}
