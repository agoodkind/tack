package ops

import (
	"regexp"
	"strings"

	"goodkind.io/tack/internal/config"
)

// backupURLCredentials matches stored FoundationDB backup credentials.
var backupURLCredentials = regexp.MustCompile(`(?i)blobstore://[^\s]*@`)

// redactSecret removes configured and stored S3 credentials from tool output.
func redactSecret(cfg *config.Config, text string) string {
	first := cfg.BackupS3SecretKey
	second := cfg.BackupS3AccessKey
	if len(second) > len(first) {
		first, second = second, first
	}

	for _, credential := range []string{first, second} {
		if credential != "" {
			text = strings.ReplaceAll(text, credential, "***REDACTED***")
		}
	}

	return backupURLCredentials.ReplaceAllString(text, "blobstore://***REDACTED***:***REDACTED***@")
}
