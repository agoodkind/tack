package searchaccess

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
)

// EncodeKey hashes length-prefixed policy and grant parts into an opaque key.
func EncodeKey(version string, parts ...[]byte) (string, error) {
	if version == "" || len(parts) == 0 {
		return "", fmt.Errorf("encode search access key: version and byte parts are required")
	}
	for _, part := range parts {
		if len(part) == 0 {
			return "", fmt.Errorf("encode search access key: byte parts must be nonempty")
		}
	}
	encoded := make([]byte, 0)
	appendPart := func(part []byte) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		encoded = append(encoded, length[:]...)
		encoded = append(encoded, part...)
	}
	appendPart([]byte(version))
	for _, part := range parts {
		appendPart(part)
	}
	digest := sha256.Sum256(encoded)
	return version + ":" + base64.RawURLEncoding.EncodeToString(digest[:]), nil
}
