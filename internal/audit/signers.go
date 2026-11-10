package audit

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// KeyIdentifier is the one name a signing key has everywhere: the notarizer
// stamps it on every audit.notarizations row, the export writes it into the
// manifest, and the valid signer set lists it. It is the first eight bytes of
// the SHA-256 of the raw Ed25519 public key, in hex, under the "ed25519:"
// prefix. One derivation, so a key measured on a host is the same string the
// ledger holds for it (TACK-437).
func KeyIdentifier(pub ed25519.PublicKey) string {
	digest := sha256.Sum256(pub)
	return keyIdentifierPrefix + hex.EncodeToString(digest[:keyIdentifierBytes])
}

const (
	keyIdentifierPrefix = "ed25519:"
	keyIdentifierBytes  = 8
)

// keyIdentifierShape is what a well-formed identifier looks like; a set entry
// that does not match is a typo, and a typo in an allowlist is a silent hole.
var keyIdentifierShape = regexp.MustCompile(`^ed25519:[0-9a-f]{16}$`)

// SignerSet is the set of signing-key identifiers an environment accepts. A
// notarization or an export manifest signed under any other identifier is
// rejected by verification and reported, so a leaked key is contained by
// removing its identifier from the set (TACK-437). The set is configured per
// environment through AUDIT_VALID_SIGNERS; an empty value is no set at all,
// which verification treats as unconfigured rather than as an empty allowlist.
type SignerSet struct {
	ordered []string
	members map[string]bool
}

// ParseSignerSet reads a comma-separated list of identifiers. Whitespace
// around an entry is ignored; an entry of the wrong shape is an error rather
// than a silently unmatched member.
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

// Configured reports whether the set names at least one signer.
func (s SignerSet) Configured() bool { return len(s.ordered) > 0 }

// Allows reports whether the identifier is in the set.
func (s SignerSet) Allows(id string) bool { return s.members[id] }

// Identifiers returns the members in the order they were listed.
func (s SignerSet) Identifiers() []string {
	out := make([]string, len(s.ordered))
	copy(out, s.ordered)
	return out
}
