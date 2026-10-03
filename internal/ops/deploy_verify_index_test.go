package ops

import (
	"testing"
)

const (
	givenIndexDigest = "sha256:04fa9f9300000000000000000000000000000000000000000000000000000001"
	otherIndexDigest = "sha256:5a02a7d200000000000000000000000000000000000000000000000000000002"
	upperIndexDigest = "sha256:04FA9F9300000000000000000000000000000000000000000000000000000001"
	sha512SameHex    = "sha512:04fa9f9300000000000000000000000000000000000000000000000000000001"
)

// TestParseDeployIndexDigestsRequiresBothInIndexForm requires both flags or
// neither, each as sha256 and 64 lowercase hex digits.
func TestParseDeployIndexDigestsRequiresBothInIndexForm(t *testing.T) {
	if digests, err := parseDeployIndexDigests(t.Context(), "", ""); err != nil || digests != nil {
		t.Fatalf("no flags = %v, %v, want the local tag comparison", digests, err)
	}
	digests, err := parseDeployIndexDigests(t.Context(), givenIndexDigest, otherIndexDigest)
	if err != nil || digests[appContainer] != givenIndexDigest || digests[auditConsumerContainer] != otherIndexDigest {
		t.Fatalf("both flags = %v, %v", digests, err)
	}
	refused := [][2]string{
		{givenIndexDigest, ""},
		{"", otherIndexDigest},
		{upperIndexDigest, otherIndexDigest},
		{sha512SameHex, otherIndexDigest},
		{"04fa9f93", otherIndexDigest},
	}
	for _, flags := range refused {
		if _, err := parseDeployIndexDigests(t.Context(), flags[0], flags[1]); err == nil {
			t.Errorf("flags %q were accepted", flags)
		}
	}
}
