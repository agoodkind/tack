// Package ops: deploy_verify_test.go covers the digest comparator the deploy
// gate relies on. The compareDigests function is a pure function over two
// strings; tests exercise the canonical equal case, the trailing-prefix
// stripping, the empty-string failure mode, and the case-folding of hex.

package ops

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// TestDeployVerifyTargetsFollowTheRenderedTag pins the two images a deploy
// rolls, at the tag the environment pins unless the operator names one, so
// the check reads the same references the compose file resolves.
func TestDeployVerifyTargetsFollowTheRenderedTag(t *testing.T) {
	cfg := &config.Config{DeployRegistry: "ghcr.io/agoodkind/", DeployImageTag: "a6af885"}
	got := deployVerifyTargets(cfg, "")
	want := []deployVerifyTarget{
		{Container: "tack-app-1", Image: "ghcr.io/agoodkind/tack-server:a6af885"},
		{Container: "tack-audit-consumer-1", Image: "ghcr.io/agoodkind/tack-audit-consumer:a6af885"},
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("target %d = %v, want %v", i, got[i], want[i])
		}
	}
	explicit := deployVerifyTargets(cfg, " 2eb962e ")
	wantExplicit := []deployVerifyTarget{
		{Container: "tack-app-1", Image: "ghcr.io/agoodkind/tack-server:2eb962e"},
		{Container: "tack-audit-consumer-1", Image: "ghcr.io/agoodkind/tack-audit-consumer:2eb962e"},
	}
	if len(explicit) != len(wantExplicit) {
		t.Fatalf("explicit targets = %v, want %v", explicit, wantExplicit)
	}
	for i := range wantExplicit {
		if explicit[i] != wantExplicit[i] {
			t.Fatalf("explicit target %d = %v, want %v", i, explicit[i], wantExplicit[i])
		}
	}
}

func TestCompareDigestsEqual(t *testing.T) {
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	err := compareDigests("tack-app-1", digest, digest)
	if err != nil {
		t.Fatalf("expected equal digests to pass, got: %v", err)
	}
}

func TestCompareDigestsBareHexEqualsAlgoPrefix(t *testing.T) {
	hex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd00"
	err := compareDigests("tack-app-1", "sha256:"+hex, hex)
	if err != nil {
		t.Fatalf("expected sha256:hex == hex, got: %v", err)
	}
}

func TestCompareDigestsCaseInsensitive(t *testing.T) {
	hex := "ABCdef00112233445566778899aabbccddeeff00112233445566778899aabbcc"
	err := compareDigests("tack-app-1", "sha256:"+strings.ToLower(hex), "sha256:"+hex)
	if err != nil {
		t.Fatalf("expected case-insensitive match, got: %v", err)
	}
}

func TestCompareDigestsRepoDigestForm(t *testing.T) {
	digest := "sha256:11ee22dd33cc44bb55aa6699887766554433221100ffeeddccbbaa9988776655"
	repoDigest := "ghcr.io/agoodkind/tack@" + digest
	err := compareDigests("tack-app-1", repoDigest, digest)
	if err != nil {
		t.Fatalf("expected repo-digest@sha256 to match bare digest, got: %v", err)
	}
}

func TestCompareDigestsMismatchFails(t *testing.T) {
	expected := "sha256:" + strings.Repeat("a", 64)
	actual := "sha256:" + strings.Repeat("b", 64)
	err := compareDigests("tack-app-1", expected, actual)
	if err == nil {
		t.Fatal("expected mismatch to error")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected mismatch wording, got: %v", err)
	}
	if !strings.Contains(err.Error(), "tack-app-1") {
		t.Fatalf("expected container name in error, got: %v", err)
	}
}

func TestCompareDigestsEmptyFails(t *testing.T) {
	err := compareDigests("tack-app-1", "", "sha256:abc")
	if err == nil {
		t.Fatal("expected empty expected to fail")
	}
	err = compareDigests("tack-app-1", "sha256:abc", "")
	if err == nil {
		t.Fatal("expected empty actual to fail")
	}
}

func TestNormalizeDigestStripsAlgoPrefix(t *testing.T) {
	got := normalizeDigest("sha256:ABC")
	if got != "abc" {
		t.Fatalf("expected lowercased hex without prefix, got %q", got)
	}
}

func TestNormalizeDigestHandlesRepoDigest(t *testing.T) {
	got := normalizeDigest("ghcr.io/x/y@sha256:DEF")
	if got != "def" {
		t.Fatalf("expected stripped repo-digest, got %q", got)
	}
}

func TestNormalizeDigestEmpty(t *testing.T) {
	got := normalizeDigest("   ")
	if got != "" {
		t.Fatalf("expected empty result for whitespace, got %q", got)
	}
}
