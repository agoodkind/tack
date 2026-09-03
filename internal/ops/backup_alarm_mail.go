// backup_alarm_mail.go mails a backup fault from inside the check that found
// it, rather than from a systemd OnFailure= handler outside it. The handler
// piped the failed run's journal to msmtp under the guest's plain hostname;
// SMTP2GO accepted every one of those with 250 OK and delivered none of them,
// so the alarm was reporting success while telling nobody. The mailer here
// sends from the address that demonstrably delivers, and it speaks SMTP itself
// over net/smtp after parsing the msmtp account file, so no mail binary is
// executed and the repo's no-shell-outs rule holds. The words the mail carries
// come from backup_alarm_words.go; when to send is decided in
// backup_alarm_policy.go.

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

// mailBackupStalenessAlarm mails the faults that began on this run and reports
// whether the transport accepted the message, which is what the caller records
// so an unsent mail is retried on the next run. It returns no error on purpose.
// The staleness error is the run's verdict, and a mail that could not be sent
// must never replace or mask it, so every failure here ends in a log line
// instead of an error the caller could mistake for the verdict. A mail path
// that fails quietly is the defect this alarm exists to remove, so each failure
// is logged at Error with its reason.
func mailBackupStalenessAlarm(ctx context.Context, cfg *config.Config, faults []backupStalenessMetric) bool {
	logger := telemetry.L(ctx)
	names := make([]string, 0, len(faults))
	for _, fault := range faults {
		names = append(names, fault.Name)
	}
	if cfg.BackupAlarmEmail == "" {
		logger.WarnContext(ctx, "backup.staleness.alarm_recipient_unset",
			slog.String("reason", "TACK_BACKUP_ALARM_EMAIL is empty, so the fault was printed but not mailed"),
			slog.Any("metrics", names))
		return false
	}

	host := backupAlarmHost()
	message := mailer.Message{
		To:      cfg.BackupAlarmEmail,
		Subject: backupStalenessAlarmSubject(host, faults),
		Body:    backupStalenessAlarmBody(cfg, host, faults),
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
			slog.Any("metrics", names),
			slog.String("err", err.Error()))
		return false
	}
	logger.InfoContext(ctx, "backup.staleness.alarm_sent",
		slog.String("to", cfg.BackupAlarmEmail),
		slog.Any("metrics", names))
	return true
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
