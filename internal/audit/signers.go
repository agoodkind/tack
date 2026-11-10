package audit

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// KeyIdentifier returns the shared identifier for an Ed25519 signing key.
//
// The notarizer stores this identifier in audit.notarizations, exports store
// it in the manifest, and the valid signer set uses it. TACK-437 established
// this shared derivation to match a key checked on a host with audit rows.
//
//   - The identifier starts with ed25519:.
//   - Its suffix encodes the first eight bytes of the raw public key's SHA-256
//     hash in hexadecimal.
func KeyIdentifier(pub ed25519.PublicKey) string {
	digest := sha256.Sum256(pub)
	return keyIdentifierPrefix + hex.EncodeToString(digest[:keyIdentifierBytes])
}

const (
	keyIdentifierPrefix = "ed25519:"
	keyIdentifierBytes  = 8
)

// keyIdentifierShape checks the identifier format. A malformed signer-set
// entry could otherwise fail to match silently.
var keyIdentifierShape = regexp.MustCompile(`^ed25519:[0-9a-f]{16}$`)

// SignerSet contains the signing-key identifiers that an environment accepts.
// Verification reports and rejects notarizations or export manifests under
// other identifiers. Removing a leaked key's identifier excludes that key
// from the accepted set (TACK-437).
//
// AUDIT_VALID_SIGNERS configures the set per environment. An empty value
// leaves verification unconfigured; it does not configure an empty allowlist.
type SignerSet struct {
	ordered []string
	members map[string]bool
}

// ParseSignerSet reads a comma-separated list of signing-key identifiers.
//
// The parser trims whitespace around each entry and rejects malformed entries
// instead of accepting entries that cannot match a signer.
//
//   - Empty entries do not add members.
//   - Duplicate entries keep their first position.
func ParseSignerSet(raw string) (SignerSet, error) {
	set := SignerSet{ordered: nil, members: map[string]bool{}}

	for entry := range strings.SplitSeq(raw, ",") {
		id := strings.TrimSpace(entry)
		if id == "" {
			continue
		}

		if !keyIdentifierShape.MatchString(id) {
			return SignerSet{ordered: nil, members: nil},
				fmt.Errorf("signer set entry %q: want ed25519: followed by sixteen hex digits", id)
		}

		if set.members[id] {
			continue
		}

		set.members[id] = true
		set.ordered = append(set.ordered, id)
	}

	return set, nil
}

// Configured reports whether the set contains at least one signer.
//
// VerifySigners checks configuration before checking the local public key.
//
//   - VerifySigners returns [ErrNoSignerSet] for an empty set after confirming
//     that its reader and pool are nonnil.
func (s SignerSet) Configured() bool { return len(s.ordered) > 0 }

// Allows reports whether id belongs to the set.
//
// The method checks membership using the exact identifier string.
//
//   - An absent identifier returns false.
func (s SignerSet) Allows(id string) bool { return s.members[id] }

// Identifiers returns the members in their input order.
//
// The method copies the stored slice before returning it.
//
//   - Changing the returned slice does not change the set's order.
func (s SignerSet) Identifiers() []string {
	out := make([]string, len(s.ordered))
	copy(out, s.ordered)

	return out
}
