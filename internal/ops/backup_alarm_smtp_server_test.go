package ops

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The account the test msmtprc names. The password is a literal written to a
// temporary file the test deletes; no real credential reaches this package.
const (
	alarmSMTPUser     = "alarm"
	alarmSMTPPassword = "not-a-real-password" // gitleaks:allow test placeholder
	alarmSMTPFrom     = "tack-test-mailer@goodkind.io"
)

// writeAlarmMsmtprc writes the account file the alarm parses, pointing at a
// local listener. The host is spelled "localhost" because net/smtp refuses
// PLAIN authentication over an unencrypted connection to anywhere else.
func writeAlarmMsmtprc(t *testing.T, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "msmtprc")
	body := "defaults\n" +
		"tls off\n" +
		"tls_starttls off\n" +
		"account test\n" +
		"host localhost\n" +
		"port " + strconv.Itoa(port) + "\n" +
		"user " + alarmSMTPUser + "\n" +
		"password " + alarmSMTPPassword + "\n" +
		"from " + alarmSMTPFrom + "\n" +
		"account default : test\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write msmtprc: %v", err)
	}
	return path
}

// alarmSMTPServer is a submission server on loopback. It is the relay's side of
// the production conversation rather than a substitute for the alarm: the alarm
// composes the real message and net/smtp speaks the real protocol to it, and
// the server hands back the bytes that left the guest.
type alarmSMTPServer struct {
	listener net.Listener
	received chan string
}

// startAlarmSMTPServer listens on loopback and serves one submission per
// connection.
func startAlarmSMTPServer(t *testing.T) *alarmSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	server := &alarmSMTPServer{listener: listener, received: make(chan string, 4)}
	t.Cleanup(func() { _ = listener.Close() })
	go server.serve()
	return server
}

// port is the loopback port the account file must name.
func (s *alarmSMTPServer) port(t *testing.T) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("split listener address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse listener port %q: %v", portText, err)
	}
	return port
}

func (s *alarmSMTPServer) serve() {
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.serveConnection(connection)
	}
}

// serveConnection answers the submission commands net/smtp sends and publishes
// the message body once the client closes the data phase.
func (s *alarmSMTPServer) serveConnection(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	reader := bufio.NewReader(connection)
	write := func(line string) bool {
		_, err := connection.Write([]byte(line + "\r\n"))
		return err == nil
	}
	if !write("220 localhost ESMTP test") {
		return
	}
	var message strings.Builder
	inData := false
	for {
		raw, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line := strings.TrimRight(raw, "\r\n")
		if inData {
			if line == "." {
				inData = false
				s.received <- message.String()
				if !write("250 2.0.0 queued") {
					return
				}
				continue
			}
			// A line the client stuffed with a leading dot is delivered
			// without it, so the captured bytes are what the mailbox shows.
			message.WriteString(strings.TrimPrefix(line, ".") + "\n")
			continue
		}
		if !s.answerCommand(line, write, &inData) {
			return
		}
	}
}

// answerCommand replies to one submission command and reports whether the
// conversation continues.
func (s *alarmSMTPServer) answerCommand(line string, write func(string) bool, inData *bool) bool {
	switch {
	case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
		return write("250-localhost") && write("250 AUTH PLAIN LOGIN")
	case strings.HasPrefix(line, "AUTH"):
		return write("235 2.7.0 accepted")
	case strings.HasPrefix(line, "DATA"):
		*inData = true
		return write("354 end with <CRLF>.<CRLF>")
	case strings.HasPrefix(line, "QUIT"):
		_ = write("221 2.0.0 bye")
		return false
	default:
		return write("250 2.0.0 ok")
	}
}
