package testenv

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// certFileVariable is the variable that replaces the system bundle search
// list of [crypto/x509] on Linux.
const certFileVariable = "SSL_CERT_FILE"

// systemRootFiles is the Linux bundle search list of [crypto/x509]. Go reads
// the first file that opens and ignores the rest.
var systemRootFiles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// TrustMailpit starts this process's Mailpit server when the binary's -run
// selection includes one of tests, and points SSL_CERT_FILE at a bundle of
// the system roots and the server's certificate authority. A TestMain calls
// it before m.Run, because Go reads the system root pool once per process.
// The bundle contains the system roots. Other TLS clients in the process
// verify against them as before. A selection without those tests starts
// nothing and leaves SSL_CERT_FILE unchanged. [Mailpit] reports a failure to
// the test that asks for the server.
func TrustMailpit(ctx context.Context, tests []string) {
	if !flag.Parsed() {
		flag.Parse()
	}
	if testing.Short() || !mailpitSelected(tests) {
		return
	}
	mailpitState.once.Do(func() {
		provisionCtx, cancel := context.WithTimeout(ctx, provisionTimeout)
		defer cancel()
		fixture, err := provisionMailpit(provisionCtx)
		if err == nil {
			err = trustMailpitBundle(provisionCtx, fixture)
		}
		mailpitState.fixture, mailpitState.err, mailpitState.trusted = fixture, err, err == nil
	})
}

// mailpitSelected reports whether the top level of the -run selection
// matches one of tests. An empty selection runs every test.
func mailpitSelected(tests []string) bool {
	selection := flag.Lookup("test.run")
	if selection == nil {
		return false
	}
	pattern, _, _ := strings.Cut(selection.Value.String(), "/")
	if pattern == "" {
		return true
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(tests, expression.MatchString)
}

// trustMailpitBundle points SSL_CERT_FILE at the fixture's trust bundle.
func trustMailpitBundle(ctx context.Context, fixture MailpitFixture) error {
	if err := os.Setenv(certFileVariable, fixture.TrustBundle); err != nil {
		slog.ErrorContext(ctx, "testenv.mailpit.trust_failed", slog.String("err", err.Error()))
		return fmt.Errorf("set %s: %w", certFileVariable, err)
	}
	slog.InfoContext(ctx, "testenv.mailpit.trusted", slog.String("bundle", fixture.TrustBundle))
	return nil
}

// systemRoots returns the first system bundle file that opens, the file Go
// reads when SSL_CERT_FILE is unset. It returns nil on a host without one.
func systemRoots() []byte {
	for _, candidate := range systemRootFiles {
		contents, err := os.ReadFile(candidate)
		if err == nil {
			return append(contents, '\n')
		}
	}
	return nil
}
