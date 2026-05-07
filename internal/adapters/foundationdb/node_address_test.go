package foundationdb

import (
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"goodkind.io/tack/internal/domain/node"
)

func TestAddressIndexKeyUsesGenericAddressTuple(t *testing.T) {
	packed := addressIndexKey("project", node.AddressKindPrimary, "TACK")
	unpacked, err := tuple.Unpack(stripPrefix(packed))
	if err != nil {
		t.Fatalf("unpack address key: %v", err)
	}
	if len(unpacked) != 4 {
		t.Fatalf("address tuple len = %d, want 4", len(unpacked))
	}
	if unpacked[0] != keyAddressIndex {
		t.Fatalf("address tuple prefix = %v, want %s", unpacked[0], keyAddressIndex)
	}
	if unpacked[1] != "project" {
		t.Fatalf("address tuple node type = %v, want project", unpacked[1])
	}
	if unpacked[2] != string(node.AddressKindPrimary) {
		t.Fatalf("address tuple kind = %v, want %s", unpacked[2], node.AddressKindPrimary)
	}
	if unpacked[3] != "TACK" {
		t.Fatalf("address tuple address = %v, want TACK", unpacked[3])
	}
}
