package audit

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// KeyIdentifier derives the identifier used by notarization rows, export
// manifests, and valid signer sets (TACK-437). It prefixes the hexadecimal
// encoding of the first eight SHA-256 digest bytes with "ed25519:".
// The digest covers the raw Ed25519 public key.
func KeyIdentifier(pub ed25519.PublicKey) string {
	digest := sha256.Sum256(pub)
	return keyIdentifierPrefix + hex.EncodeToString(digest[:keyIdentifierBytes])
}

const (
	keyIdentifierPrefix = "ed25519:"
	keyIdentifierBytes  = 8
)

// keyIdentifierShape defines the accepted format for signer identifiers.
var keyIdentifierShape = regexp.MustCompile(`^ed25519:[0-9a-f]{16}$`)

// SignerSet defines the signing-key identifiers an environment accepts.
// Verification rejects notarizations and export manifests signed under other
// identifiers. Remove an identifier to reject a leaked key (TACK-437).
// AUDIT_VALID_SIGNERS configures the set per environment. Verification treats
// an empty value as unconfigured rather than as an empty allowlist.
type SignerSet struct {
	ordered []string
	members map[string]bool
}

// ParseSignerSet reads a comma-separated list of identifiers. It trims
// whitespace around each entry and rejects entries with an invalid format.
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

// Configured reports whether the set includes at least one signer.
func (s SignerSet) Configured() bool { return len(s.ordered) > 0 }

// Allows reports whether the identifier is in the set.
func (s SignerSet) Allows(id string) bool { return s.members[id] }

// Identifiers returns the members in their original order.
func (s SignerSet) Identifiers() []string {
	out := make([]string, len(s.ordered))
	copy(out, s.ordered)

	return out
}
