package foundationdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// searchProjectionFormat identifies the fields that each contribution
// hashes for the digest in a projection state record. A change to those
// fields requires a larger value.
const searchProjectionFormat = 2

const (
	contributionDefinition = "definition"
	contributionType       = "type"
)

// definitionContributionInput defines the definition fields that a
// projection contribution hashes: ID, Name, Type, Options,
// AppliesToFeatures, ReferenceTargetTypeKey, and Search.
type definitionContributionInput struct {
	Kind                   string                 `json:"kind"`
	ID                     uuid.UUID              `json:"id"`
	Name                   string                 `json:"name"`
	Type                   node.PropertyType      `json:"type"`
	Options                []node.EnumOption      `json:"options"`
	AppliesToFeatures      []string               `json:"applies_to_features"`
	ReferenceTargetTypeKey string                 `json:"reference_target_type_key"`
	Search                 *node.SearchProjection `json:"search"`
}

// typeContributionInput defines the node type fields that a projection
// contribution hashes: ID, TypeKey, and Features.
type typeContributionInput struct {
	Kind     string    `json:"kind"`
	ID       uuid.UUID `json:"id"`
	TypeKey  string    `json:"type_key"`
	Features []string  `json:"features"`
}

// definitionContribution hashes the rendering inputs of one property
// definition. It returns nil for a nil definition.
func definitionContribution(ctx context.Context, definition *node.PropertyDef) ([]byte, error) {
	if definition == nil {
		return nil, nil
	}
	return hashContribution(ctx, "property definition "+definition.ID.String(), definitionContributionInput{
		Kind: contributionDefinition, ID: definition.ID, Name: definition.Name, Type: definition.Type,
		Options: definition.Options, AppliesToFeatures: sortedCopy(definition.AppliesToFeatures),
		ReferenceTargetTypeKey: definition.ReferenceTargetTypeKey, Search: definition.Search,
	})
}

// typeContribution hashes kind's inputs for property applicability.
// It returns nil for a nil node type.
func typeContribution(ctx context.Context, kind *node.NodeType) ([]byte, error) {
	if kind == nil {
		return nil, nil
	}
	return hashContribution(ctx, "node type "+kind.ID.String(), typeContributionInput{
		Kind: contributionType, ID: kind.ID, TypeKey: kind.TypeKey, Features: sortedCopy(kind.Features),
	})
}

func hashContribution[Input definitionContributionInput | typeContributionInput](ctx context.Context, subject string, input Input) ([]byte, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, searchReadFailure(ctx, "encode projection contribution of "+subject, err)
	}
	sum := sha256.Sum256(encoded)
	return sum[:], nil
}

// replaceContribution subtracts previous from the hex digest and adds next
// modulo 2^256. It treats a nil contribution or an empty digest as zero.
func replaceContribution(ctx context.Context, digest string, previous, next []byte) (string, error) {
	stored, err := hex.DecodeString(digest)
	if err != nil {
		return "", searchReadFailure(ctx, "decode projection digest", err)
	}
	return hex.EncodeToString(sumContributions(stored, previous, next)), nil
}

// sumContributions subtracts previous from stored and adds next modulo
// 2^256. It returns projectionDigestBytes bytes.
func sumContributions(stored, previous, next []byte) []byte {
	sum := new(big.Int).SetBytes(stored)
	if previous != nil {
		sum.Sub(sum, new(big.Int).SetBytes(previous))
	}
	if next != nil {
		sum.Add(sum, new(big.Int).SetBytes(next))
	}
	sum.Mod(sum, projectionModulus)
	return sum.FillBytes(make([]byte, projectionDigestBytes))
}

func sortedCopy(values []string) []string {
	copied := slices.Clone(values)
	slices.Sort(copied)
	return copied
}
