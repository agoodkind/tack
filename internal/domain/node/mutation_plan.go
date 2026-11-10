package node

import (
	"context"
	"crypto/sha256"
	"encoding/json"

	"github.com/google/uuid"
)

// MutationEdgeLimit is the maximum number of relationships in each direction
// that DeleteNode deletes in one transaction.
const MutationEdgeLimit = 100

// Digest is the SHA-256 digest of one stored encoding.
type Digest [32]byte

// PrimaryDigest returns the SHA-256 digest of the exact stored node_instance
// bytes.
func PrimaryDigest(encoding []byte) Digest {
	return sha256.Sum256(encoding)
}

// RelationshipDigest returns the SHA-256 digest of the exact stored metadata
// bytes of one relationship.
func RelationshipDigest(metadata []byte) Digest {
	return sha256.Sum256(metadata)
}

// RelationshipSnapshot represents one stored relationship and the digest of
// its stored metadata.
type RelationshipSnapshot struct {
	Relationship Relationship
	Digest       Digest
}

// MutationSnapshot represents the state of one node that one FoundationDB
// transaction read. Exists is false if the node does not have a primary record
// in the organization. Outgoing lists every relationship from the node.
// Incoming lists every relationship to the node if the count does not exceed
// MutationEdgeLimit. Otherwise, IncomingOverflow is true and Incoming is empty.
type MutationSnapshot struct {
	Exists           bool
	Node             Node
	Encoding         []byte
	PrimaryDigest    Digest
	ParentID         uuid.UUID
	Outgoing         []RelationshipSnapshot
	Incoming         []RelationshipSnapshot
	IncomingOverflow bool
}

// SchemaSnapshot represents the schema and metadata epoch that one transaction
// read for one organization.
type SchemaSnapshot struct {
	Schema
	MetadataEpoch int64
}

// SourceExpectation specifies the source state that a mutation requires.
// A plan must set exactly one of Absent and PrimaryDigest.
type SourceExpectation struct {
	Absent        bool
	PrimaryDigest Digest
}

// GraphRecord specifies the expected primary digest of one node that
// preparation read.
type GraphRecord struct {
	NodeID        uuid.UUID
	PrimaryDigest Digest
}

// RelationshipExpectation specifies the expected metadata digest of one
// outgoing relationship that preparation read.
type RelationshipExpectation struct {
	RelationType string
	TargetID     uuid.UUID
	Digest       Digest
}

// IncomingRelationshipExpectation specifies the expected metadata digest of
// one relationship to the mutated node that preparation read.
type IncomingRelationshipExpectation struct {
	SourceID     uuid.UUID
	RelationType string
	Digest       Digest
}

// GraphExpectation specifies the expected digests of records and relationships
// that preparation read. Records lists the proposed parent, each ancestor of
// that parent, and each referenced node. Relationships lists the outgoing
// relationships that the mutation removes or deletes. Incoming lists the
// relationships to a node that the mutation deletes.
type GraphExpectation struct {
	Records       []GraphRecord
	Relationships []RelationshipExpectation
	Incoming      []IncomingRelationshipExpectation
}

// ParentChange requests a hierarchy parent.
type ParentChange struct {
	TargetID uuid.UUID
}

// MutationPlan specifies one checked node create or update. Props specifies
// a partial patch that preserves omitted properties and removes properties
// with a JSON null. Parent sets parent_id and the child_of edge. A nil Parent
// preserves the current parent. Relationships changes relationships of other
// types.
type MutationPlan struct {
	OrgID         uuid.UUID
	NodeID        uuid.UUID
	NodeType      string
	ActorID       uuid.UUID
	Source        SourceExpectation
	MetadataEpoch int64
	Graph         GraphExpectation
	Name          *string
	Props         map[string]json.RawMessage
	Parent        *ParentChange
	ReferenceKeys []ReferenceKey
	Relationships RelationshipChanges
	Idempotency   *IdempotencyRecord
	Checkpoint    *CheckpointWrite `exhaustruct:"optional"`
}

// DeletionPlan specifies one checked deletion of exactly one node.
type DeletionPlan struct {
	OrgID      uuid.UUID
	NodeID     uuid.UUID
	ActorID    uuid.UUID
	Source     SourceExpectation
	Graph      GraphExpectation
	Checkpoint *CheckpointWrite `exhaustruct:"optional"`
}

// CheckpointWrite updates a repair checkpoint in the same transaction as
// one checked mutation. The transaction writes Next only when the stored
// phase equals Previous.
type CheckpointWrite struct {
	Previous RepairPhase
	Next     RepairCheckpoint
}

// MutationReceipt reports the committed transaction attempt. If a create
// matches an existing idempotency record, NodeID identifies the existing node
// and SourceDigest is zero.
type MutationReceipt struct {
	OrgID        uuid.UUID
	NodeID       uuid.UUID
	SourceDigest Digest
	ResultDigest Digest
}

// Mutator reads mutation snapshots and applies checked mutations. Each
// transaction attempt rereads every expectation. If an expectation differs,
// the attempt returns an error that wraps domain.ErrConflict without writing.
type Mutator interface {
	ReadMutationSnapshot(ctx context.Context, orgID, nodeID uuid.UUID) (MutationSnapshot, SchemaSnapshot, error)
	ApplyMutation(ctx context.Context, plan MutationPlan) (MutationReceipt, error)
	DeleteNode(ctx context.Context, plan DeletionPlan) (MutationReceipt, error)
}

// MutableNodeRepository provides the node storage operations that the node
// service uses to read and mutate nodes.
type MutableNodeRepository interface {
	NodeRepository
	Mutator
}
