package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"goodkind.io/tack/internal/domain/node"
)

func checkFrozenSource(ctx context.Context, entry NodeIntegrityEntry) error {
	source := entry.Source
	if source == nil {
		return repairFrozenSourceMissingError{RepairID: entry.RepairID}
	}

	if source.PrimaryDigest != primaryDigest(source.Primary) {
		message := fmt.Sprintf(
			"repair %s primary digest does not match its frozen primary record",
			entry.RepairID,
		)

		return frozenValidationError(message)
	}

	var primary node.Node
	if decodeErr := json.Unmarshal(source.Primary, &primary); decodeErr != nil {
		message := fmt.Sprintf(
			"repair %s frozen primary record is not a node record: %s",
			entry.RepairID,
			decodeErr.Error(),
		)

		return frozenValidationError(message)
	}

	if primary.ID != entry.NodeID || primary.NodeType != entry.NodeType {
		message := fmt.Sprintf(
			"repair %s frozen primary record is node %s of type %q, not node %s of type %q",
			entry.RepairID,
			primary.ID,
			primary.NodeType,
			entry.NodeID,
			entry.NodeType,
		)

		return frozenValidationError(message)
	}

	if !strings.HasPrefix(source.MetadataEpoch, primary.OrgID.String()+"=") {
		message := fmt.Sprintf(
			"repair %s metadata epoch %q does not belong to organization %s",
			entry.RepairID,
			source.MetadataEpoch,
			primary.OrgID,
		)

		return frozenValidationError(message)
	}

	digest, err := graphDigest(ctx, *source, false)
	if err != nil {
		return err
	}

	if source.GraphDigest != digest {
		message := fmt.Sprintf(
			"repair %s graph digest does not match its frozen edges, related records, and chain",
			entry.RepairID,
		)

		return frozenValidationError(message)
	}

	return nil
}

type repairFrozenSourceMissingError struct {
	RepairID string
}

func (failure repairFrozenSourceMissingError) Error() string {
	return fmt.Sprintf("repair %s does not include a frozen source", failure.RepairID)
}

type repairNodeIDMissingError struct {
	RepairID string
}

func (failure repairNodeIDMissingError) Error() string {
	return fmt.Sprintf("repair %s does not specify node_id", failure.RepairID)
}

type frozenValidationError string

func (failure frozenValidationError) Error() string {
	return string(failure)
}
