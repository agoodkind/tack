package search

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// MaxPageBytes is the Tack-enforced UTF-8 byte bound of one page_text value.
const MaxPageBytes = 4096

// DocumentID returns a stable identifier for one revision-bound search page.
func DocumentID(orgID, nodeID uuid.UUID, revision, projection string, ordinal uint64) string {
	var identity bytes.Buffer
	writeIdentityPart(&identity, orgID[:])
	writeIdentityPart(&identity, nodeID[:])
	writeIdentityPart(&identity, []byte(revision))
	writeIdentityPart(&identity, []byte(projection))
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], ordinal)
	writeIdentityPart(&identity, number[:])
	return base64.RawURLEncoding.EncodeToString(identity.Bytes())
}

func writeIdentityPart(destination *bytes.Buffer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	destination.Write(length[:])
	destination.Write(value)
}

// ValidatePageText rejects an active page without page_text or with text
// above the byte bound, before any engine request.
func ValidatePageText(text string) error {
	if text == "" {
		return errors.New("an active search page requires page_text")
	}
	if len(text) > MaxPageBytes || !utf8.ValidString(text) {
		return fmt.Errorf("search page_text must be valid UTF-8 at or below %d bytes", MaxPageBytes)
	}
	return nil
}

// ValidatePage checks document invariants before a write intent is registered.
// Live work and replacement copy work write pages. Every other class returns
// ErrWorkChanged.
func ValidatePage(work Work, page node.ContentPage) error {
	writesPages := work.Class == WorkClassLive || work.Class == WorkClassCopy
	if !writesPages || work.Target == "" || work.Generation <= 0 {
		return ErrWorkChanged
	}
	if page.NodeID != work.NodeID || page.Revision == "" || page.ProjectionVersion == "" {
		return ErrWorkChanged
	}
	if err := ValidatePageText(page.Text); err != nil {
		return err
	}
	if page.OverlapBytes < 0 || page.OverlapBytes > MaxPageBytes/4 || page.OverlapBytes >= len(page.Text) {
		return errors.New("search page overlap exceeds the reserved byte bound or does not advance")
	}
	if page.Access.Generation != work.Generation {
		return ErrWorkChanged
	}
	return ValidateAccess(page.Access)
}

// ValidateAccess rejects empty, unsorted, or duplicate opaque access values.
func ValidateAccess(access node.SearchAccess) error {
	if len(access.Versions) == 0 || len(access.Keys) == 0 {
		return errors.New("search access versions and keys must not be empty")
	}
	if !slices.IsSorted(access.Versions) || !slices.IsSorted(access.Keys) {
		return errors.New("search access values must be sorted")
	}
	if hasDuplicate(access.Versions) || hasDuplicate(access.Keys) {
		return errors.New("search access values must be unique")
	}
	if slices.Contains(access.Versions, "") || slices.Contains(access.Keys, "") {
		return errors.New("search access values must not be empty")
	}
	return nil
}

func hasDuplicate(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index] == values[index-1] {
			return true
		}
	}
	return false
}
