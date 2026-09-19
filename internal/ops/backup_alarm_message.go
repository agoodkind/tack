// backup_alarm_message.go builds the message the alarm hands to the relay. It
// exists because the mail library's own renderer appends a footer that lists
// the guest's public address, the name of the company that sells it the line,
// and every address on every interface including the docker bridges, and that
// footer cannot be turned off: mailer.Mailer.Send calls CollectSysInfo and
// RenderHTML unconditionally, and the package exports no seam between them
// (mailer.go Send, sysinfo.go CollectSysInfo in
// goodkind.io/send-email@v0.0.0-20260805215029-28988072a5b0). An alarm states
// what stopped, since when and what to check; a host's addresses in a mailbox
// are the same class of leak as an endpoint. So the parts are built here, out
// of the same words the operator reads, and the footer carries only the run,
// the guest and the send time. The account file is still the library's to
// parse; delivery is in backup_alarm_smtp.go.

package ops

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/send-email/mailer"
)

// backupAlarmFromDomain is the domain the alarm sends from. The address is
// <guest>-mailer@<domain>, the sender the relay demonstrably delivers, rather
// than the guest's plain hostname address it accepted and dropped.
const backupAlarmFromDomain = "goodkind.io"

// backupAlarmSendTimeLayout prints the send time with its zone beside it. The
// time is UTC, like every other time in the mail, so nothing in the message is
// read as the reader's local clock.
const backupAlarmSendTimeLayout = "2006-01-02 15:04:05 MST"

// backupAlarmHTMLTemplate is the HTML part: the body as the operator wrote it,
// then the three rows the operator needs to act. It names no address.
const backupAlarmHTMLTemplate = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<style>
body { font: 14px -apple-system, BlinkMacSystemFont, Arial, sans-serif; margin: 0 }
.meta { margin-top: 16px; padding-top: 12px; border-top: 1px solid rgba(128,128,128,0.2); font-size: 11px; opacity: 0.6 }
.meta table { border-collapse: collapse }
.meta td { padding: 1px 0 }
.meta .k { padding-right: 12px; opacity: 0.7 }
</style>
</head>
<body>
<div>{{range .BodyLines}}{{.}}<br>
{{end}}</div>
<div class="meta">
<table>
  <tr><td class="k">Caller</td><td>{{.Caller}}</td></tr>
  <tr><td class="k">Time</td><td>{{.SentAt}}</td></tr>
  <tr><td class="k">Host</td><td>{{.Host}}</td></tr>
</table>
</div>
</body>
</html>
`

// backupAlarmHTMLData is what the HTML part prints.
type backupAlarmHTMLData struct {
	BodyLines []string
	Caller    string
	SentAt    string
	Host      string
}

// backupAlarmFromAddress is the sender for a mail from this guest.
func backupAlarmFromAddress(host string) string {
	return host + "-mailer@" + backupAlarmFromDomain
}

// backupAlarmCallerName is the run the message reports, falling back to the
// library's own name for a message that declares none.
func backupAlarmCallerName(message mailer.Message) string {
	if caller := strings.TrimSpace(message.Caller); caller != "" {
		return caller
	}
	return backupAlarmCaller
}

// backupAlarmTextPart is the body followed by the run, the guest and the send
// time, the footer shape the alarm has always carried in its plain-text part.
func backupAlarmTextPart(message mailer.Message, host string, sentAt time.Time) string {
	return message.Body + "\n\nCaller: " + backupAlarmCallerName(message) +
		"\nHost: " + host +
		"\nTime: " + sentAt.UTC().Format(backupAlarmSendTimeLayout) + "\n"
}

// backupAlarmHTMLPart is the same message as markup. Every value is escaped,
// because a probe's detail reaches the body and nothing in it is markup.
func backupAlarmHTMLPart(message mailer.Message, host string, sentAt time.Time) (string, error) {
	parsed, err := template.New("backup-alarm").Parse(backupAlarmHTMLTemplate)
	if err != nil {
		return "", failBackupAlarmRender("parse the backup alarm mail template", err)
	}
	var rendered bytes.Buffer
	data := backupAlarmHTMLData{
		BodyLines: strings.Split(message.Body, "\n"),
		Caller:    backupAlarmCallerName(message),
		SentAt:    sentAt.UTC().Format(backupAlarmSendTimeLayout),
		Host:      host,
	}
	if err := parsed.Execute(&rendered, data); err != nil {
		return "", failBackupAlarmRender("render the backup alarm mail", err)
	}
	return rendered.String(), nil
}

// failBackupAlarmRender names the rendering step that failed, logs it, and
// returns it. A message that could not be built never reaches the relay, so the
// reason belongs in the journal beside the alarm the run could not send.
func failBackupAlarmRender(step string, err error) error {
	slog.Error("backup.staleness.alarm_render_failed",
		slog.String("step", step),
		slog.String("err", err.Error()))
	return fmt.Errorf("%s: %w", step, err)
}

// backupAlarmMIME is the submission the relay receives: the headers, the plain
// text part, and the HTML part, in one multipart/alternative message. The
// boundary is a fresh identifier, so no body can close the message early.
func backupAlarmMIME(message mailer.Message, host string, sentAt time.Time) ([]byte, error) {
	html, err := backupAlarmHTMLPart(message, host, sentAt)
	if err != nil {
		return nil, err
	}
	boundary := "=_tack_alarm_" + uuid.NewString()
	var mime strings.Builder
	fmt.Fprintf(&mime, "From: %s <%s>\r\n", host, backupAlarmFromAddress(host))
	fmt.Fprintf(&mime, "To: %s\r\n", message.To)
	fmt.Fprintf(&mime, "Subject: %s\r\n", message.Subject)
	mime.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&mime, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&mime, "--%s\r\n", boundary)
	mime.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	mime.WriteString(backupAlarmTextPart(message, host, sentAt))
	mime.WriteString("\r\n\r\n")
	fmt.Fprintf(&mime, "--%s\r\n", boundary)
	mime.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	mime.WriteString(html)
	mime.WriteString("\r\n\r\n")
	fmt.Fprintf(&mime, "--%s--\r\n", boundary)
	return []byte(mime.String()), nil
}
