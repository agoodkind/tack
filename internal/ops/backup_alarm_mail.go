// The staleness check sends its own backup alarm. The earlier systemd
// OnFailure= handler submitted the failed run's journal to msmtp with the
// guest's plain hostname sender. The original incident account records SMTP2GO
// responses of 250 OK without message delivery. This history explains the
// default -mailer@ sender that the alarm retains.
//
// The mailer parses the msmtp account file and uses net/smtp without executing
// a mail binary, as the repository requires. The alarm word composers build
// the message from the vocabulary, and the transition policy selects when to
// send it.

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

// backupAlarmCaller identifies the command in the library's rendered message,
// including when an operator reads the message on a phone.
const backupAlarmCaller = "tack ops backup staleness-check"

// backupAlarmUnknownHost identifies a guest when hostname lookup fails or
// returns blank text. A missing hostname must not prevent an alarm message.
const backupAlarmUnknownHost = "unknown-host"

// backupAlarmSendFunc sends one alarm message. Tests replace this package
// variable, as they replace nowFunc, to check that a send failure does not mask
// the staleness error without running an SMTP server.
var backupAlarmSendFunc = sendBackupAlarmMail

// mailBackupStalenessAlarm sends new faults and reports a successful send
// result. The caller records success and retries after an error on a later
// run. A QUIT error can follow server acceptance of DATA; a false result does
// not prove server refusal. A retry can repeat an accepted message.
// The boolean result does not replace the run's staleness error. Send failures
// log their reasons at Error level; an unset recipient logs a warning without
// attempting a send.
func mailBackupStalenessAlarm(
	ctx context.Context,
	cfg *config.Config,
	faults []backupStalenessMetric,
) bool {
	logger := telemetry.L(ctx)

	names := make([]string, 0, len(faults))
	for _, fault := range faults {
		names = append(names, fault.Name)
	}

	if cfg.BackupAlarmEmail == "" {
		logger.WarnContext(
			ctx,
			"backup.staleness.alarm_recipient_unset",
			slog.String(
				"reason",
				"TACK_BACKUP_ALARM_EMAIL is empty, so the fault was printed but not mailed",
			),
			slog.Any("metrics", names),
		)

		return false
	}

	message := mailer.Message{
		To:      cfg.BackupAlarmEmail,
		Subject: backupStalenessAlarmSubject(backupAlarmHost(), faults),
		Body:    backupStalenessAlarmBody(cfg, faults),
		// An empty From selects <hostname>-mailer@goodkind.io in the mailer. The
		// original incident reported delivery with this sender and accepted but
		// undelivered mail with the guest's plain hostname sender.
		From:   "",
		Name:   "",
		Caller: backupAlarmCaller,
	}
	if err := backupAlarmSendFunc(ctx, cfg, message); err != nil {
		logger.ErrorContext(
			ctx,
			"backup.staleness.alarm_undelivered",
			slog.String("to", cfg.BackupAlarmEmail),
			slog.Any("metrics", names),
			slog.String("err", err.Error()),
		)

		return false
	}

	logger.InfoContext(
		ctx,
		"backup.staleness.alarm_sent",
		slog.String("to", cfg.BackupAlarmEmail),
		slog.Any("metrics", names),
	)

	return true
}

// sendBackupAlarmMail uses the SMTP account in the msmtp file. Explicit
// sendmail transport selects that credential source rather than auto transport;
// an SMTP2GO key in the environment cannot select the HTTP API instead.
func sendBackupAlarmMail(ctx context.Context, cfg *config.Config, message mailer.Message) error {
	sender := mailer.New(mailer.Config{
		Transport:   mailer.MethodSendmail,
		MsmtprcPath: cfg.BackupAlarmMsmtprcPath,
	})
	if err := sender.Send(ctx, message); err != nil {
		wrapped := fmt.Errorf("mail backup staleness alarm to %s: %w", message.To, err)
		// This log records the account file and transport error. The caller logs the
		// alarm result for the affected run.
		telemetry.L(ctx).ErrorContext(
			ctx,
			"backup.staleness.alarm_transport_failed",
			slog.String("msmtprc", cfg.BackupAlarmMsmtprcPath),
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}

// backupAlarmHost returns the hostname of the guest that observed the metric.
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
