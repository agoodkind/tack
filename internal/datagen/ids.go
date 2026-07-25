package datagen

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

var identityNamespace = uuid.MustParse("7ac0face-cafe-7000-8000-000000000245")

func deterministicUUID(key string) uuid.UUID {
	return uuid.NewSHA1(identityNamespace, []byte(key))
}

func deterministicUUIDv7(key string) uuid.UUID {
	sum := sha256.Sum256([]byte(key))
	var raw [16]byte
	copy(raw[:], sum[:16])
	milliseconds := binary.BigEndian.Uint64(sum[:8]) % 4102444800000
	raw[0] = byte(milliseconds >> 40)
	raw[1] = byte(milliseconds >> 32)
	raw[2] = byte(milliseconds >> 24)
	raw[3] = byte(milliseconds >> 16)
	raw[4] = byte(milliseconds >> 8)
	raw[5] = byte(milliseconds)
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	return uuid.UUID(raw)
}

func tokenFor() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		slog.Error("qa.datagen.token_entropy_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read token entropy: %w", err)
	}
	return "tack_qa_" + hex.EncodeToString(raw[:]), nil
}

func deterministicTokenFor(seed int64, key string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", seed, key)))
	return "tack_qa_" + hex.EncodeToString(sum[:24])
}
