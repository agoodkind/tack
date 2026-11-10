package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

var serviceSecretNames = []string{
	"DATABASE_URL", "OPENSEARCH_PASSWORD", "SEED_API_TOKEN",
	"AUDIT_WRITER_DSN", "AUDIT_READER_DSN", "AUDIT_REDACTOR_DSN", "AUDIT_OPERATOR_DSN",
	"AUDIT_WRITER_PASSWORD", "AUDIT_READER_PASSWORD", "AUDIT_REDACTOR_PASSWORD", "AUDIT_OPERATOR_PASSWORD",
	"TACK_APP_PASSWORD", "TACK_MIGRATOR_PASSWORD",
	"TACK_BACKUP_S3_ACCESS_KEY_ID", "TACK_BACKUP_S3_SECRET_ACCESS_KEY",
	"AUDIT_CONSUMER_YUGABYTE_DSN",
}

// secretEnvironment resolves explicit secret references without changing the
// process environment. Only the configuration parser receives resolved values.
func secretEnvironment(names []string) (map[string]string, error) {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	for _, name := range names {
		path, referenced := environment[name+"_FILE"]
		if !referenced {
			continue
		}
		if environment[name] != "" {
			return nil, fmt.Errorf("configure %s or %s_FILE, not both", name, name)
		}
		if path == "" {
			return nil, fmt.Errorf("%s_FILE requires a file path", name)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			wrapped := fmt.Errorf("read %s_FILE: %w", name, err)
			slog.Error("config.secret_file_failed", slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		value := strings.TrimRight(string(contents), "\r\n")
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s_FILE contains an empty secret", name)
		}
		environment[name] = value
	}
	return environment, nil
}
