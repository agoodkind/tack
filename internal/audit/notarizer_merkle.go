package audit

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type chainHead struct {
	shard int16
	seq   int64
	hash  []byte
}

// merkleAndManifest computes the Merkle root over chain heads and returns the
// JSON manifest stored in audit.notarizations.
func merkleAndManifest(list []chainHead) ([]byte, []byte) {
	sort.Slice(list, func(i, j int) bool { return list[i].shard < list[j].shard })

	leaves := make([][]byte, 0, len(list))
	manifestRows := make([]map[string]any, 0, len(list))
	for _, h := range list {
		hasher := sha256.New()
		hasher.Write([]byte{0x00})
		var b [10]byte
		binary.BigEndian.PutUint16(b[0:2], uint16(h.shard))
		binary.BigEndian.PutUint64(b[2:10], uint64(h.seq))
		hasher.Write(b[:])
		hasher.Write(h.hash)
		leaves = append(leaves, hasher.Sum(nil))
		manifestRows = append(manifestRows, map[string]any{
			"shard":     h.shard,
			"last_seq":  h.seq,
			"last_hash": hex.EncodeToString(h.hash),
		})
	}
	root := merkleRoot(leaves)
	manifestJSON, _ := json.Marshal(manifestRows)
	return root, manifestJSON
}

func merkleRoot(leaves [][]byte) []byte {
	if len(leaves) == 0 {
		empty := sha256.Sum256(nil)
		return empty[:]
	}
	level := leaves
	for len(level) > 1 {
		next := make([][]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			hasher := sha256.New()
			hasher.Write([]byte{0x01})
			hasher.Write(level[i])
			if i+1 < len(level) {
				hasher.Write(level[i+1])
			} else {
				hasher.Write(level[i])
			}
			next = append(next, hasher.Sum(nil))
		}
		level = next
	}
	return level[0]
}
