package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
)

const (
	// mailpitImage is the Mailpit release that alarm mail tests deliver to,
	// pinned by tag and by the digest of its multi-platform image index.
	mailpitImage = "axllent/mailpit:v1.31.3@sha256:ed9b00c609e77e99c79b93f1178255ebc271868920f2c69a8d166bd5634ed10d"
	// mailpitSMTPPort is the SMTP listener, which offers STARTTLS.
	mailpitSMTPPort = 1025
	// mailpitAPIPort is the HTTP API listener.
	mailpitAPIPort = "8025"
	// mailpitHolderSeconds is how long the address holder sleeps, which is
	// longer than any test binary runs.
	mailpitHolderSeconds = "2147483647"
	// mailpitCertificatePath and mailpitKeyPath are where the engine reads its
	// STARTTLS certificate and key.
	mailpitCertificatePath = "/etc/mailpit/smtp.pem"
	mailpitKeyPath         = "/etc/mailpit/smtp-key.pem"
	// mailpitUser and mailpitSender are the login and envelope sender of the
	// generated msmtp account. The engine accepts any login.
	mailpitUser   = "tack-testenv"
	mailpitSender = "tack-testenv@example.test"
	// mailpitPasswordBytes is the random size of the generated password.
	mailpitPasswordBytes = 16
)

// mailpitEnvironment binds both listeners on the engines' IPv4 network,
// serves STARTTLS with the generated certificate, refuses mail before
// STARTTLS, and accepts any SMTP login.
var mailpitEnvironment = []string{
	"MP_SMTP_BIND_ADDR=0.0.0.0:" + strconv.Itoa(mailpitSMTPPort),
	"MP_UI_BIND_ADDR=0.0.0.0:" + mailpitAPIPort,
	"MP_SMTP_TLS_CERT=" + mailpitCertificatePath,
	"MP_SMTP_TLS_KEY=" + mailpitKeyPath,
	"MP_SMTP_REQUIRE_STARTTLS=true",
	"MP_SMTP_AUTH_ACCEPT_ANY=true",
}

// mailpitMsmtprcFormat is the msmtp account file the production mailer
// parses: the host, port, sender, user, and password, in that order.
const mailpitMsmtprcFormat = `account testenv
host %s
port %d
from %s
auth on
user %s
password %s
tls on
tls_starttls on

account default : testenv
`

// MailpitFixture is this process's real SMTP server. The production mailer
// connects to SMTPHost, runs STARTTLS, verifies the server certificate for
// SMTPHost against the system root pool, and then logs in. A test package
// that uses the fixture calls [TrustMailpit] in TestMain before any TLS use,
// because Go reads the system root pool once per process. SMTPHost is the
// holder container's address on the engines' network, the address the Kafka
// fixture also uses, and the certificate lists that address. A test process on
// the Docker host or on the engines' network connects to it without a name
// lookup.
type MailpitFixture struct {
	SMTPHost string
	SMTPPort int
	// Msmtprc is the path of an msmtp account file, mode 0600, that sends
	// through the fixture with STARTTLS and a login.
	Msmtprc string
	// CA is the path of the PEM certificate authority that signed the SMTP
	// certificate.
	CA string
	// TrustBundle is the path of a PEM bundle of the system roots and CA.
	TrustBundle string
	// APIBaseURL is the HTTP API root, without a trailing slash.
	APIBaseURL string
}

var mailpitState struct {
	once    sync.Once
	fixture MailpitFixture
	err     error
	// trusted is true once SSL_CERT_FILE points at a bundle with the CA.
	trusted bool
}

// Mailpit returns the Mailpit server that [TrustMailpit] started for this
// process. Every test in the process shares the server. A test that counts
// delivered mail calls [MailpitFixture.DeleteAll] before it sends. The test
// fails when TestMain did not start the server for it.
func Mailpit(t T) MailpitFixture {
	t.Helper()
	skipWhenShort(t)
	if mailpitState.err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", mailpitState.err)
		t.FailNow()
	}
	if !mailpitState.trusted {
		_, _ = fmt.Fprintln(t.Output(), "testenv: no Mailpit server is trusted; TestMain must call testenv.TrustMailpit with this test's name")
		t.FailNow()
	}
	return mailpitState.fixture
}

// provisionMailpit starts the engine and returns the fixture once the HTTP
// API reports ready. A holder container that only sleeps starts first. The
// SMTP certificate is issued for the holder's container name. The engine then
// starts in the holder's network stack.
func provisionMailpit(ctx context.Context) (MailpitFixture, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return MailpitFixture{}, err
	}
	defer func() { _ = cli.Close() }()
	holder, err := startEngine(ctx, cli, engineSpec{
		kind: "mailpit-address", image: mailpitImage, platform: nil,
		entrypoint: []string{"sleep"}, cmd: []string{mailpitHolderSeconds}, env: nil,
	})
	if err != nil {
		return MailpitFixture{}, err
	}
	authority, err := newClusterAuthority(ctx)
	if err != nil {
		return MailpitFixture{}, err
	}
	certificate, err := authority.issue(ctx, holder.name, []string{holder.name, holder.address})
	if err != nil {
		return MailpitFixture{}, err
	}
	started, err := startEngine(ctx, cli, engineSpec{
		kind: "mailpit", image: mailpitImage, platform: nil, cmd: nil, env: mailpitEnvironment,
		files:     map[string][]byte{mailpitCertificatePath: certificate.certificate, mailpitKeyPath: certificate.key},
		networkOf: holder.name,
	})
	if err != nil {
		return MailpitFixture{}, err
	}
	password, err := randomHex(ctx, mailpitPasswordBytes)
	if err != nil {
		return MailpitFixture{}, err
	}
	msmtprc := fmt.Appendf(nil, mailpitMsmtprcFormat, holder.address, mailpitSMTPPort, mailpitSender, mailpitUser, password)
	directory, err := writeMailpitClientFiles(ctx, holder.name, msmtprc, authority.pem)
	if err != nil {
		return MailpitFixture{}, err
	}
	fixture := MailpitFixture{
		SMTPHost: holder.address, SMTPPort: mailpitSMTPPort,
		Msmtprc: filepath.Join(directory, "msmtprc"), CA: filepath.Join(directory, "ca.pem"),
		TrustBundle: filepath.Join(directory, "trust-bundle.pem"),
		APIBaseURL:  "http://" + net.JoinHostPort(holder.address, mailpitAPIPort),
	}
	if err := waitForMailpit(ctx, fixture); err != nil {
		return MailpitFixture{}, engineStartFailure(ctx, cli, started.name, err)
	}
	slog.InfoContext(ctx, "testenv.mailpit.ready", slog.String("container", started.name), slog.String("smtp_host", holder.address))
	return fixture, nil
}

// writeMailpitClientFiles writes the msmtp account file, the CA certificate,
// and the trust bundle, each mode 0600, to a directory of their own under the
// user cache directory, and records the directory for [Release].
func writeMailpitClientFiles(ctx context.Context, containerName string, msmtprc, caPEM []byte) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	directory := filepath.Join(cacheRoot, "tack-testenv", containerName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		slog.ErrorContext(ctx, "testenv.mailpit.client_files_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("create %s: %w", directory, err)
	}
	ownDirectory(directory)
	trustBundle := slices.Concat(systemRoots(), caPEM)
	for name, contents := range map[string][]byte{"msmtprc": msmtprc, "ca.pem": caPEM, "trust-bundle.pem": trustBundle} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			slog.ErrorContext(ctx, "testenv.mailpit.client_files_failed", slog.String("err", err.Error()))
			return "", fmt.Errorf("write %s: %w", path, err)
		}
	}
	return directory, nil
}
