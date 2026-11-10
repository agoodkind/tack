package ops

import (
	"strings"

	"goodkind.io/tack/internal/config"
)

// redactSecret removes both S3 credentials before tool output enters logs.
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
	return text
}
