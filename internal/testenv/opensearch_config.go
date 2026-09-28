package testenv

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/bcrypt"
	"goodkind.io/tack/internal/telemetry"
)

// openSearchConfig enables REST and transport TLS with the fixture's own CA.
// The security plugin initializes its index from the bundled configuration
// files, including the generated internal users file, on first start. ML
// Commons runs the model on this single data node. action.auto_create_index
// refuses automatic creation of every node-pages-* index, as each production
// node configuration does. A delayed bulk write to a deleted physical index
// then fails.
const openSearchConfig = `cluster.name: tack-testenv
action.auto_create_index: "-node-pages-*,+*"
node.name: opensearch
network.host: 0.0.0.0
plugins.security.ssl.http.enabled: true
plugins.security.ssl.http.pemcert_filepath: certs/node.pem
plugins.security.ssl.http.pemkey_filepath: certs/node-key.pem
plugins.security.ssl.http.pemtrustedcas_filepath: certs/ca.pem
plugins.security.ssl.transport.pemcert_filepath: certs/node.pem
plugins.security.ssl.transport.pemkey_filepath: certs/node-key.pem
plugins.security.ssl.transport.pemtrustedcas_filepath: certs/ca.pem
plugins.security.ssl.transport.enforce_hostname_verification: false
plugins.security.nodes_dn:
  - "CN=opensearch-node"
plugins.security.allow_default_init_securityindex: true
plugins.ml_commons.only_run_on_ml_node: false
`

// openSearchInternalUsersFormat replaces the image's internal users. The
// bundled role mapping grants all_access to the admin backend role.
const openSearchInternalUsersFormat = `_meta:
  type: "internalusers"
  config_version: 2
%s:
  hash: "%s"
  reserved: true
  backend_roles:
    - "admin"
  description: "Tack test administrator"
`

// openSearchInternalUsers returns an internal users file with one record for
// user. The record stores a bcrypt hash of password.
func openSearchInternalUsers(ctx context.Context, user, password string) ([]byte, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		wrapped := fmt.Errorf("hash OpenSearch fixture password: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.fixture_password_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return fmt.Appendf(nil, openSearchInternalUsersFormat, user, hash), nil
}
