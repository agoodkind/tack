package audit

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func TestCanonicalJSONIsDeterministic(t *testing.T) {
	a := map[string]any{"b": 2, "a": 1, "nested": map[string]any{"y": []any{3, 2, 1}, "x": true}}
	b := map[string]any{"nested": map[string]any{"x": true, "y": []any{3, 2, 1}}, "a": 1, "b": 2}

	ca, err := canonicalJSON(a)
	if err != nil {
		t.Fatalf("canonicalJSON a: %v", err)
	}
	cb, err := canonicalJSON(b)
	if err != nil {
		t.Fatalf("canonicalJSON b: %v", err)
	}
	if !bytes.Equal(ca, cb) {
		t.Fatalf("equal logical inputs produced different canonical bytes:\n a=%s\n b=%s", ca, cb)
	}
}

func TestHashRowChainsPreviousHash(t *testing.T) {
	prev := []byte("seed")
	h1, err := hashRow(prev, map[string]any{"k": 1})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hashRow(prev, map[string]any{"k": 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(h1, h2) {
		t.Errorf("hashRow not deterministic")
	}
	h3, err := hashRow(append(prev, 'x'), map[string]any{"k": 1})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(h1, h3) {
		t.Errorf("hashRow ignored prevHash")
	}
}

func TestShardOfStableAndBucketed(t *testing.T) {
	a := uuid.MustParse("019dd220-440e-729a-a442-281aaf73ca30")
	e := uuid.MustParse("019dd221-440e-729a-a442-281aaf73ca30")
	got := shardOf(a, e)
	if shardOf(a, e) != got {
		t.Errorf("shardOf not stable")
	}
	if got < 0 || got > 255 {
		t.Errorf("shardOf out of [0,255]: %d", got)
	}
}
