package foundationdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"math/big"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// projectionDigestBytes is the width of a stored projection digest.
const projectionDigestBytes = sha256.Size

// projectionModulus bounds a digest sum to projectionDigestBytes bytes.
var projectionModulus = new(big.Int).Lsh(big.NewInt(1), projectionDigestBytes*8)

type projectionDeclaration struct {
	ID     uuid.UUID              `json:"id"`
	Name   string                 `json:"name"`
	Search *node.SearchProjection `json:"search"`
}

// declarationHash hashes the legacy projection inputs of definition. It
// returns nil for a nil definition. Releases before projection state
// records read the legacy digest, which sums these hashes.
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

// updateProjectionDigest subtracts the previous legacy declaration hash
// and adds the next one modulo 2^256. It does not read or write when the
// hashes are equal. Otherwise, it reads and writes one key.
func updateProjectionDigest(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, previous, next []byte) error {
	if bytes.Equal(previous, next) {
		return nil
	}
	stored, err := tr.Get(fdb.Key(searchProjectionKey(orgID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read projection digest", err)
	}
	tr.Set(fdb.Key(searchProjectionKey(orgID)), sumContributions(stored, previous, next))
	return nil
}
