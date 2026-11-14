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

// ResourceTarget returns a resource audit target for uri.
//
// The target stores uri as its URI and sets its kind to
// TargetKindResource. The other fields are empty or zero UUIDs.
//
//	target := ResourceTarget(uri)
func ResourceTarget(uri string) Target {
	return Target{
		Kind:         TargetKindResource,
		NodeType:     "",
		Reference:    "",
		Name:         "",
		URI:          uri,
		RelationType: "",
		ID:           uuid.Nil,
		SourceID:     uuid.Nil,
		TargetID:     uuid.Nil,
	}
}

// ExternalTarget returns the external target for reference.
//
// The target uses TargetKindExternal and records the reference without
// a node ID.
//
// Example:
//
//	target := ExternalTarget("audit/verify-bundle")
//	target.Reference == "audit/verify-bundle"
func ExternalTarget(reference string) Target {
	return Target{
		Kind:         TargetKindExternal,
		NodeType:     "",
		Reference:    reference,
		Name:         "",
		URI:          "",
		RelationType: "",
		ID:           uuid.Nil,
		SourceID:     uuid.Nil,
		TargetID:     uuid.Nil,
	}
}

// NodeTarget returns a resource audit target for nodeID.
//
// The target URI appends the string form of nodeID to NodeURIPrefix.
// ResourceTarget sets the kind to TargetKindResource and leaves the
// remaining fields empty or zero UUIDs.
//
//	target := NodeTarget(nodeID)
func NodeTarget(nodeID uuid.UUID) Target {
	return ResourceTarget(NodeURIPrefix + nodeID.String())
}

// EventTarget returns a resource audit target for eventID.
//
// The target URI appends the string form of eventID to EventURIPrefix.
// ResourceTarget sets the kind to TargetKindResource and leaves the
// remaining fields empty or zero UUIDs.
//
//	target := EventTarget(eventID)
func EventTarget(eventID uuid.UUID) Target {
	return ResourceTarget(EventURIPrefix + eventID.String())
}

// CanonicalUUID returns the canonical UUID string for value.
//
// CanonicalUUID trims surrounding whitespace before parsing value. It
// returns an empty string when parsing fails.
//
//	id := CanonicalUUID(value)
func CanonicalUUID(value string) string {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}

	return id.String()
}

// ResourceURI returns a resource URI for prefix and value.
//
// ResourceURI appends the canonical UUID form of value to prefix. When
// value is not a UUID, ResourceURI appends InvalidURISegment instead.
//
//	uri := ResourceURI(prefix, value)
func ResourceURI(prefix, value string) string {
	id := CanonicalUUID(value)
	if id == "" {
		return prefix + InvalidURISegment
	}

	return prefix + id
}
