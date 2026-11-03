module goodkind.io/tack/postgresql-overlay

go 1.27.1

toolchain go1.27.3

// The pinned upstream release requires older versions of the three modules
// below. These versions match the root Tack module and include the fixes for
// the vulnerabilities that govulncheck reports for the older versions.
require (
	github.com/hashicorp/terraform-plugin-sdk/v2 v2.40.1
	golang.org/x/net v0.61.0
	golang.org/x/text v0.43.0
	google.golang.org/grpc v1.84.0
)
