// backup_alarm_mail.go mails the backup staleness report from inside the check
// that produced it, rather than from a systemd OnFailure= handler outside it.
// The handler piped the failed run's journal to msmtp under the guest's plain
// hostname; SMTP2GO accepted every one of those with 250 OK and delivered none
// of them, so the alarm was reporting success while telling nobody. The mailer
// here sends from the address that demonstrably delivers, and it speaks SMTP
// itself over net/smtp after parsing the msmtp account file, so no mail binary
// is executed and the repo's no-shell-outs rule holds.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"goodkind.io/send-email/mailer"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// backupAlarmCaller names this command in the mail the library renders, so a
// message read on a phone says which run produced it.
const backupAlarmCaller = "tack ops backup staleness-check"

// backupAlarmUnknownHost stands in when the host cannot name itself. Naming the
// guest must never cost the mail: the alarm exists to make a silent backup
// failure loud, so it still sends when it cannot say where from.
const backupAlarmUnknownHost = "unknown-host"

// backupAlarmSendFunc delivers one alarm message. It is a package variable for
// the same reason nowFunc is: tests substitute it to prove a send failure never
// masks the staleness error, without standing up an SMTP server.
var backupAlarmSendFunc = sendBackupAlarmMail

// mailBackupStalenessAlarm mails the report the run just printed. It returns
// nothing on purpose. The staleness error is the run's verdict, and a mail that
// could not be sent must never replace or mask it, so every failure here ends
// in a log line instead of an error the caller could mistake for the verdict.
// A mail path that fails quietly is the defect this whole change exists to
// remove, so each failure is logged at Error with its reason.
func mailBackupStalenessAlarm(ctx context.Context, cfg *config.Config, report string, stale []string) {
	logger := telemetry.L(ctx)
	if cfg.BackupAlarmEmail == "" {
		logger.WarnContext(ctx, "backup.staleness.alarm_recipient_unset",
			slog.String("reason", "TACK_BACKUP_ALARM_EMAIL is empty, so the stale report was printed but not mailed"))
		return
	}

	host := backupAlarmHost()
	message := mailer.Message{
		To:      cfg.BackupAlarmEmail,
		Subject: backupStalenessAlarmSubject(host),
		Body:    backupStalenessAlarmBody(host, report, stale),
		// From is deliberately empty: the library then sends as
		// <hostname>-mailer@goodkind.io, the sender whose mail actually
		// arrives, instead of the guest's plain hostname address that the
		// relay accepted and dropped.
		From:   "",
		Name:   "",
		Caller: backupAlarmCaller,
	}
	if err := backupAlarmSendFunc(ctx, cfg, message); err != nil {
		logger.ErrorContext(ctx, "backup.staleness.alarm_undelivered",
			slog.String("to", cfg.BackupAlarmEmail),
			slog.Int("stale_count", len(stale)),
			slog.String("err", err.Error()))
		return
	}
	logger.InfoContext(ctx, "backup.staleness.alarm_sent",
		slog.String("to", cfg.BackupAlarmEmail),
		slog.Int("stale_count", len(stale)))
}

// sendBackupAlarmMail delivers the message over the SMTP account in the msmtp
// file. The transport is pinned to sendmail rather than left on auto so the
// account file is the one credential source, and an SMTP2GO key that happens to
// sit in the environment cannot silently reroute the alarm onto the HTTP API.
func sendBackupAlarmMail(ctx context.Context, cfg *config.Config, message mailer.Message) error {
	sender := mailer.New(mailer.Config{
		Transport:   mailer.MethodSendmail,
		MsmtprcPath: cfg.BackupAlarmMsmtprcPath,
	})
	if err := sender.Send(ctx, message); err != nil {
		wrapped := fmt.Errorf("mail backup staleness alarm to %s: %w", message.To, err)
		// The transport detail is logged here and the alarm-level verdict at
		// the caller, so a mail that never left names both the account file it
		// read and the run it was reporting.
		telemetry.L(ctx).ErrorContext(ctx, "backup.staleness.alarm_transport_failed",
			slog.String("msmtprc", cfg.BackupAlarmMsmtprcPath),
			slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// backupAlarmHost names the guest the reading came from.
func backupAlarmHost() string {
	host, err := os.Hostname()
	if err != nil {
		return backupAlarmUnknownHost
	}
	if strings.TrimSpace(host) == "" {
		return backupAlarmUnknownHost
	}
	return host
}

// backupStalenessAlarmSubject is the one line a reader sees before opening
// anything, so it carries the verdict and the guest it came from.
func backupStalenessAlarmSubject(host string) string {
	return "[tack] backups are stale on " + host
}

// backupStalenessAlarmBody names the stale mechanisms and then carries the
// per-metric report verbatim, so the mail and the terminal say the same thing
// and neither has to be trusted over the other.
func backupStalenessAlarmBody(host, report string, stale []string) string {
	var body strings.Builder
	fmt.Fprintf(&body, "%d backup mechanism(s) on %s are past their staleness threshold: %s\n\n",
		len(stale), host, strings.Join(stale, ", "))
	body.WriteString(report)
	return body.String()
}
