//go:build searchconversion

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func verifyGeneratedSearchHashes(t *testing.T, directory string) {
	t.Helper()
	for _, name := range []string{"OPENSEARCH_PASSWORD", "OPENSEARCH_MAINTENANCE_PASSWORD", "OPENSEARCH_HEALTH_PASSWORD"} {
		password, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		hashed, err := os.ReadFile(filepath.Join(directory, name+".hash"))
		if err != nil {
			t.Fatal(err)
		}
		_, value, found := strings.Cut(strings.TrimSpace(string(hashed)), ":")
		if !found {
			t.Fatalf("the generated %s hash file lacks its username delimiter", name)
		}
		normalized := strings.TrimRight(string(password), "\r\n")
		if err := bcrypt.CompareHashAndPassword([]byte(value), []byte(normalized)); err != nil {
			t.Fatalf("the generated %s hash does not authenticate the runtime-normalized credential", name)
		}
	}
	t.Log("All three generated search role hashes authenticate the runtime-normalized credentials.")
}
