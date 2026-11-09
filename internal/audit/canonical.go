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

// canonicalJSON returns deterministic bytes for hashing v. It sorts object
// keys and uses encoding/json to encode numbers, strings, booleans, and null.
// It approximates RFC 8785 (JCS, the JSON Canonicalization Scheme) without an
// extra dependency. Matching logical values produce matching hash inputs.
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

// ShardCount sets the number of parallel hash chains for each org.
// The ledger assigns each write to a shard with shardOf(actor, eventID).
// Hash-linked event rows in audit.events form each (org, shard) chain.
// audit.chain_heads stores each chain's current head.
//
// The count can change for future writes. Every audit.events row stores
// its shard. Verification checks each stored (org, shard) chain after a
// count change rather than assigning old rows to new shards. Existing chains
// remain valid after the change. The change does not rewrite old rows.
//
// The storage and assignment constraints are:
//
//   - The shard column uses int16, with capacity beyond the current count.
//   - The Kafka key encodes the shard in two bytes, also beyond this count.
//   - The Kafka partition count is independent of ShardCount. The consumer
//     recomputes the shard from the event payload.
//   - ShardCount must be a power of two. shardOf masks a checksum with
//     ShardCount - 1.
//
// Follow the [shard-count requirements] for a forward-only change
// (TACK-306).
//
// [shard-count requirements]: https://github.com/agoodkind/tack/blob/main/docs/reference/audit/horizontal-design.md#storage-and-delivery
const ShardCount = 256

// shardMask limits each checksum to a shard in [0, ShardCount).
const shardMask = ShardCount - 1

// The mask computes a modulus only when ShardCount is a power of two.
// Other values index beyond the one-element array and fail compilation.
var _ = [1]struct{}{}[ShardCount&shardMask]

// shardOf returns a shard in [0, ShardCount) for an actor and event ID pair.
// The per-event shard distributes writes among parallel (org, shard) hash
// chains and Kafka partitions, including writes for a frequently used actor
// or event ID. It does not represent the event's logical identity.
func shardOf(actor, eventID uuid.UUID) int16 {
	var buf [32]byte
	copy(buf[0:16], actor[:])
	copy(buf[16:32], eventID[:])

	return int16(crc32.ChecksumIEEE(buf[:]) & shardMask)
}

// hashRow hashes prevHash, the eight-byte big-endian canonical payload length,
// and the canonical payload with SHA-256. prevHash contains the previous chain
// head. The first row in each (org, shard) chain uses an empty slice.
func hashRow(prevHash []byte, payload any) ([]byte, error) {
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	h.Write(prevHash)
	// The length prefix distinguishes (prev || X) from (prev || Y) when a
	// different boundary would otherwise produce the same concatenated bytes.
	var lp [8]byte
	binary.BigEndian.PutUint64(lp[:], uint64(len(canonical)))
	h.Write(lp[:])
	h.Write(canonical)

	return h.Sum(nil), nil
}
