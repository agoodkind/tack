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
	// APIAccessURIPrefix is the prefix of the resource URI for one API token
	// record.
	APIAccessURIPrefix = "tack:api_token/"
	// InvalidURISegment is the URI segment that replaces an identifier that is
	// not a UUID.
	InvalidURISegment = "invalid"
)

// ResourceTarget identifies a resource by its complete URI.
//
// The constructor stores uri unchanged. It does not parse the URI or derive
// UUID fields from it.
//
// Layout:
//
//	Kind: TargetKindResource
//	URI:  uri
//	ID, SourceID, TargetID: uuid.Nil
func ResourceTarget(uri string) Target {
	return Target{
		Kind: TargetKindResource, NodeType: "", Reference: "", Name: "", URI: uri,
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// NodeTarget returns the resource target for the node with nodeID.
//
// The URI contains nodeID. ResourceTarget leaves the UUID fields at uuid.Nil.
//
// Layout:
//
//	tack:node/<nodeID>
func NodeTarget(nodeID uuid.UUID) Target {
	return ResourceTarget(NodeURIPrefix + nodeID.String())
}

// EventTarget returns the resource target for the audit event with eventID.
//
// The URI contains eventID. ResourceTarget leaves the UUID fields at uuid.Nil.
//
// Layout:
//
//	tack:audit-event/<eventID>
func EventTarget(eventID uuid.UUID) Target {
	return ResourceTarget(EventURIPrefix + eventID.String())
}

// CanonicalUUID returns the canonical form of value, or an empty string when
// value is not a UUID.
//
// The parser removes surrounding whitespace before parsing. Valid UUIDs use
// the standard lowercase form with hyphens.
//
// Example:
//
//	CanonicalUUID(" 550E8400-E29B-41D4-A716-446655440000 ")
//	// Result: "550e8400-e29b-41d4-a716-446655440000"
func CanonicalUUID(value string) string {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return id.String()
}

// ResourceURI returns prefix followed by the canonical form of value. When value
// is not a UUID, it returns prefix followed by InvalidURISegment.
//
// CanonicalUUID validates the identifier. The function preserves prefix and
// substitutes the fixed invalid segment for malformed identifiers.
//
// Example:
//
//	ResourceURI(NodeURIPrefix, "bad-id")
//	// Result: "tack:node/invalid"
func ResourceURI(prefix, value string) string {
	id := CanonicalUUID(value)
	if id == "" {
		return prefix + InvalidURISegment
	}
	return prefix + id
}
