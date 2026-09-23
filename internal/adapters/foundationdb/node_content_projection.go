package foundationdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// projectionDigestBytes is the width of the stored projection digest.
const projectionDigestBytes = sha256.Size

// projectionModulus bounds the digest sum to projectionDigestBytes bytes.
var projectionModulus = new(big.Int).Lsh(big.NewInt(1), projectionDigestBytes*8)

// projectionVersion reads the organization's projection digest with one
// point read and combines it with the cursor pagination version. A change to
// any definition identity, name, or declaration changes the version.
func projectionVersion(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID) (string, error) {
	digest, err := tr.Get(fdb.Key(searchProjectionKey(orgID))).Get()
	if err != nil {
		return "", searchReadFailure(ctx, "read projection digest", err)
	}
	encoded, err := json.Marshal(projectionVersionInput{Pagination: searchCursorVersion, Digest: hex.EncodeToString(digest)})
	if err != nil {
		return "", searchReadFailure(ctx, "encode projection version", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type projectionVersionInput struct {
	Pagination int    `json:"pagination"`
	Digest     string `json:"digest"`
}

type projectionDeclaration struct {
	ID     uuid.UUID              `json:"id"`
	Name   string                 `json:"name"`
	Search *node.SearchProjection `json:"search"`
}

// declarationHash hashes the fields of definition that change projected
// text. A nil definition has no hash.
func declarationHash(ctx context.Context, definition *node.PropertyDef) ([]byte, error) {
	if definition == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(projectionDeclaration{ID: definition.ID, Name: definition.Name, Search: definition.Search})
	if err != nil {
		return nil, searchReadFailure(ctx, "encode projection declaration "+definition.ID.String(), err)
	}
	sum := sha256.Sum256(encoded)
	return sum[:], nil
}

// updateProjectionDigest subtracts the previous declaration hash and adds
// the next one modulo 2^256. Each update reads and writes one key.
func updateProjectionDigest(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, previous, next []byte) error {
	stored, err := tr.Get(fdb.Key(searchProjectionKey(orgID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read projection digest", err)
	}
	sum := new(big.Int).SetBytes(stored)
	if previous != nil {
		sum.Sub(sum, new(big.Int).SetBytes(previous))
	}
	if next != nil {
		sum.Add(sum, new(big.Int).SetBytes(next))
	}
	sum.Mod(sum, projectionModulus)
	tr.Set(fdb.Key(searchProjectionKey(orgID)), sum.FillBytes(make([]byte, projectionDigestBytes)))
	return nil
}
