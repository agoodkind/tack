package ops

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"goodkind.io/tack/internal/config"
)

const retentionFDBAccountFileName = "fdb-blob-credentials.json"

type retentionFDBCredential struct {
	APIKey string `json:"api_key"`
	Secret string `json:"secret"`
}

type retentionFDBCredentials struct {
	Accounts map[string]retentionFDBCredential `json:"accounts"`
}

// retentionFDBPrivateArguments removes inline credentials before Docker stores
// command metadata. Native tools read both credentials from the protected file.
func retentionFDBPrivateArguments(cfg *config.Config, arguments []string) ([]string, map[string][]byte, error) {
	host, _, err := blobstoreHostMapping(cfg.BackupS3Endpoint)
	if err != nil {
		return nil, nil, err
	}
	endpoint, err := url.Parse("blobstore://@" + host)
	if err != nil || endpoint.Hostname() == "" {
		return nil, nil, errors.New("retention requires a valid native object-store host")
	}
	credentials := retentionFDBCredentials{Accounts: map[string]retentionFDBCredential{
		"@" + endpoint.Hostname(): {APIKey: cfg.BackupS3AccessKey, Secret: cfg.BackupS3SecretKey},
	}}
	body, err := json.Marshal(credentials)
	if err != nil {
		return nil, nil, errors.New("encode native retention credentials")
	}
	private := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		if strings.HasPrefix(argument, "blobstore://") {
			separator := strings.LastIndex(argument, "@")
			if separator < len("blobstore://") {
				return nil, nil, errors.New("retention backup URL does not identify a credential account")
			}
			argument = "blobstore://@" + argument[separator+1:]
		}
		private = append(private, argument)
	}
	return private, map[string][]byte{retentionFDBAccountFileName: body}, nil
}
