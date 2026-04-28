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

// canonicalJSON returns a deterministic byte representation of v suitable
// for hashing. Object keys are sorted; numbers, strings, bools, null are
// emitted via encoding/json's default rules. This is a minimal, dependency-
// free approximation of RFC 8785 (JCS) sufficient for our hash chain: two
// callers with the same logical value always produce the same bytes.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var any any
	if err := json.Unmarshal(raw, &any); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, any); err != nil {
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

// shardOf returns the logical shard index in [0, 256) for a (actor, event)
// pair. The pair distributes load away from a hot actor and a hot event id.
func shardOf(actor, eventID uuid.UUID) int16 {
	var buf [32]byte
	copy(buf[0:16], actor[:])
	copy(buf[16:32], eventID[:])
	return int16(crc32.ChecksumIEEE(buf[:]) & 0xFF)
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
