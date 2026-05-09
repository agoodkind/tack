package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

type addressBackfillFakeInspector struct {
	rows    []fdbadapter.InspectLegacyAddressRow
	reports map[uuid.UUID]*fdbadapter.NodeInspectionReport
}

func (fake *addressBackfillFakeInspector) QueryNodeRecords(_ context.Context, nodeID uuid.UUID) (*fdbadapter.NodeInspectionReport, error) {
	if report, ok := fake.reports[nodeID]; ok {
		return report, nil
	}
	return &fdbadapter.NodeInspectionReport{NodeID: nodeID}, nil
}

type addressBackfillFakeAddressStore struct {
	addresses map[string]uuid.UUID
	writes    []addressBackfillFakeWrite
}

type addressBackfillFakeWrite struct {
	NodeType    string
	AddressKind node.AddressKind
	Address     string
	NodeID      uuid.UUID
}

func newAddressBackfillFakeAddressStore() *addressBackfillFakeAddressStore {
	return &addressBackfillFakeAddressStore{addresses: map[string]uuid.UUID{}}
}

func (fake *addressBackfillFakeAddressStore) GetAddress(_ context.Context, nodeType string, addressKind node.AddressKind, address string) (uuid.UUID, error) {
	if nodeID, ok := fake.addresses[addressBackfillFakeAddressKey(nodeType, addressKind, address)]; ok {
		return nodeID, nil
	}
	return uuid.Nil, nil
}

func (fake *addressBackfillFakeAddressStore) WriteAddress(_ context.Context, nodeType string, addressKind node.AddressKind, address string, nodeID uuid.UUID) error {
	key := addressBackfillFakeAddressKey(nodeType, addressKind, address)
	if existingNodeID, ok := fake.addresses[key]; ok && existingNodeID != nodeID {
		return fmt.Errorf("address conflict: %w", domain.ErrAlreadyExists)
	}
	fake.addresses[key] = nodeID
	fake.writes = append(fake.writes, addressBackfillFakeWrite{NodeType: nodeType, AddressKind: addressKind, Address: address, NodeID: nodeID})
	return nil
}

func legacyAddressRow(nodeID uuid.UUID, nodeType string, address string) fdbadapter.InspectLegacyAddressRow {
	return fdbadapter.InspectLegacyAddressRow{
		LegacyKeyFamily: legacySlugIndexKeyFamily,
		NodeType:        nodeType,
		AddressKind:     legacySlugAddressKind,
		AddressValue:    address,
		OwnerID:         nodeID,
	}
}

func addressBackfillFakeAddressKey(nodeType string, addressKind node.AddressKind, address string) string {
	return nodeType + "\x00" + string(addressKind) + "\x00" + address
}
