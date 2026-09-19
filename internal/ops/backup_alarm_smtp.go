// backup_alarm_smtp.go hands one composed alarm to the relay named in the
// msmtp account file. The alarm speaks SMTP itself over net/smtp rather than
// piping a message to a mail binary, so a container needs the account file
// mounted and no msmtp installed, and the repo's no-shell-outs rule holds. The
// message it sends is built in backup_alarm_message.go.

package ops

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strconv"
	"time"

	"goodkind.io/send-email/mailer"
)

// backupAlarmDialTimeout bounds the TCP handshake to the relay. An alarm that
// cannot reach the relay must fail and be retried on the next run rather than
// hold the check open.
const backupAlarmDialTimeout = 30 * time.Second

// failBackupAlarmSMTP names the step that failed, logs it, and returns it. A
// mail that never left is the failure this alarm exists to remove, so the step
// reaches the journal even though the caller turns the whole thing into one
// line about an undelivered alarm.
func failBackupAlarmSMTP(ctx context.Context, step string, err error) error {
	slog.ErrorContext(ctx, "backup.staleness.alarm_smtp_failed",
		slog.String("step", step),
		slog.String("err", err.Error()))
	return fmt.Errorf("%s: %w", step, err)
}

// sendBackupAlarmSMTP submits one message over the account's relay.
func sendBackupAlarmSMTP(
	ctx context.Context,
	account mailer.Account,
	fromAddress, to string,
	message []byte,
) error {
	address := net.JoinHostPort(account.Host, strconv.Itoa(account.Port))
	dialer := &net.Dialer{Timeout: backupAlarmDialTimeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return failBackupAlarmSMTP(ctx, "dial the mail relay "+address, err)
	}
	defer func() { _ = connection.Close() }()

	client, err := smtp.NewClient(connection, account.Host)
	if err != nil {
		return failBackupAlarmSMTP(ctx, "open an SMTP session with "+account.Host, err)
	}
	defer func() { _ = client.Close() }()

	if err := startBackupAlarmSession(ctx, client, account, fromAddress, to); err != nil {
		return err
	}
	if err := writeBackupAlarmData(ctx, client, message); err != nil {
		return err
	}
	if err := client.Quit(); err != nil {
		return failBackupAlarmSMTP(ctx, "close the SMTP session with "+account.Host, err)
	}
	return nil
}

// startBackupAlarmSession secures the connection, authenticates, and declares
// the envelope.
func startBackupAlarmSession(
	ctx context.Context,
	client *smtp.Client,
	account mailer.Account,
	fromAddress, to string,
) error {
	if account.TLSStartTLS {
		settings := &tls.Config{ServerName: account.Host, MinVersion: tls.VersionTLS12}
		if err := client.StartTLS(settings); err != nil {
			return failBackupAlarmSMTP(ctx, "start TLS with "+account.Host, err)
		}
	}
	auth := smtp.PlainAuth("", account.User, account.Password, account.Host)
	if err := client.Auth(auth); err != nil {
		return failBackupAlarmSMTP(ctx, "authenticate to "+account.Host+" as "+account.User, err)
	}
	if err := client.Mail(fromAddress); err != nil {
		return failBackupAlarmSMTP(ctx, "offer the alarm from "+fromAddress, err)
	}
	if err := client.Rcpt(to); err != nil {
		return failBackupAlarmSMTP(ctx, "offer the alarm to "+to, err)
	}
	return nil
}

// writeBackupAlarmData sends the message body. The writer net/smtp returns
// stuffs leading dots and ends the data phase on close, so a body line that
// begins with a dot cannot end the message early.
func writeBackupAlarmData(ctx context.Context, client *smtp.Client, message []byte) error {
	writer, err := client.Data()
	if err != nil {
		return failBackupAlarmSMTP(ctx, "begin the message body", err)
	}
	if _, err := writer.Write(message); err != nil {
		return failBackupAlarmSMTP(ctx, "write the message body", err)
	}
	if err := writer.Close(); err != nil {
		return failBackupAlarmSMTP(ctx, "finish the message body", err)
	}
	return nil
}
