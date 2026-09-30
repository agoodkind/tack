package integration

import (
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
)

func recordActualServerFailure(t *testing.T, path string, environment []string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Errorf("read actual server failure evidence: %v", err)
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 65536))
	if err != nil {
		t.Errorf("read bounded actual server failure evidence: %v", err)
		return
	}
	text := string(body)
	for _, variable := range environment {
		key, value, _ := strings.Cut(variable, "=")
		key = strings.ToUpper(key)
		if value != "" && (strings.Contains(key, "PASSWORD") || strings.Contains(key, "TOKEN") || strings.Contains(key, "SECRET") || strings.Contains(key, "KEY") || strings.Contains(key, "DSN") || key == "DATABASE_URL") {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	credentials := regexp.MustCompile(`(?i)(postgres(?:ql)?|https?)://[^@\s]+@`)
	text = credentials.ReplaceAllString(text, "${1}://[redacted]@")
	for line := range strings.SplitSeq(text, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "bearer ") || strings.Contains(lower, "authorization") {
			continue
		}
		if line != "" {
			t.Logf("actual server failure evidence: %s", line)
		}
	}
}
