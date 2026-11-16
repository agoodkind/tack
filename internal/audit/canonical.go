package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"sort"

	"github.com/google/uuid"
)

// canonicalJSON sorts object keys and uses encoding/json for scalar values.
// It approximates RFC 8785 canonical JSON for the hash chain.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}

	var decodedValue any
	if err := json.Unmarshal(raw, &decodedValue); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := writeCanonical(&buf, decodedValue); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool, json.Number, float64, string:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}

		buf.Write(b)
	case []any:
		buf.WriteByte('[')

		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}

			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}

		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}

		sort.Strings(keys)
		buf.WriteByte('{')

		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}

			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}

			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}

		buf.WriteByte('}')
	default:
		return fmt.Errorf("canonicalJSON: unsupported type %T", v)
	}

	return nil
}

// ShardCount sets the number of parallel (org, shard) hash chains for new
// ledger writes. Each audit.events row stores its shard. Verification reads
// existing chain heads, including closed chains from an earlier shard count.
// The int16 column and two-byte Kafka key can represent this count. Kafka's
// partition count remains independent; the consumer derives the shard from
// the payload. shardOf requires a power of two for its checksum mask.
// Count changes apply only to future writes. Follow the forward-only procedure
// in docs/runbooks/audit/shards.md (TACK-306).
const ShardCount = 256

// shardMask turns a checksum into a shard in [0, ShardCount).
const shardMask = ShardCount - 1

// The mask is only a modulus when ShardCount is a power of two; any other
// value fails to compile here (index out of range on a one-element array).
var _ = [1]struct{}{}[ShardCount&shardMask]

// shardOf returns the load-distribution shard for a (actor, event) pair, in
// [0, ShardCount). The shard is a per-event bucket, not a logical key: it
// spreads writes across parallel per-(org, shard) hash chains and Kafka
// partitions, away from a hot actor or event id.
func shardOf(actor, eventID uuid.UUID) int16 {
	var buf [32]byte
	copy(buf[0:16], actor[:])
	copy(buf[16:32], eventID[:])

	return int16(crc32.ChecksumIEEE(buf[:]) & shardMask)
}

// hashRow returns sha256(prevHash || canonical(payload)). prevHash is the
// chain head before this row; an empty slice is used for the very first row
// per (org, shard).
func hashRow(prevHash []byte, payload any) ([]byte, error) {
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	h.Write(prevHash)
	// Length prefix prevents collision between (prev || X) and (prev || Y)
	// where X and Y differ only in where the boundary is read.
	var lp [8]byte
	binary.BigEndian.PutUint64(lp[:], uint64(len(canonical)))
	h.Write(lp[:])
	h.Write(canonical)

	return h.Sum(nil), nil
}
