// The staleness checker delivers alarm email directly through the SMTP library,
// not a systemd journal-to-msmtp handler. The relay accepted plain-hostname
// senders without delivery; the library's -mailer@ sender supports delivery.
// Account-file parsing and net/smtp avoid executing a mail CLI.

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

// backupAlarmCaller identifies the command in rendered email metadata.
const backupAlarmCaller = "tack ops backup staleness-check"

// backupAlarmUnknownHost permits alarm delivery when hostname lookup fails or
// returns blank text. Missing host identity must not suppress fault reporting.
const backupAlarmUnknownHost = "unknown-host"

// backupAlarmSendFunc permits delivery-failure testing without an SMTP server.
var backupAlarmSendFunc = sendBackupAlarmMail

// mailBackupStalenessAlarm reports transport acceptance for fault persistence.
// Failed delivery logs an Error and returns false, permitting retries without
// replacing the command's stale verdict with a transport error.
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
				"TACK_BACKUP_ALARM_EMAIL is empty; the command printed the fault without sending email",
			),
			slog.Any("metrics", names),
		)

		return false
	}

	message := mailer.Message{
		To:      cfg.BackupAlarmEmail,
		Subject: backupStalenessAlarmSubject(backupAlarmHost(), faults),
		Body:    backupStalenessAlarmBody(cfg, faults),
		// Empty From selects the library's <hostname>-mailer@goodkind.io
		// sender instead of the plain-hostname address the relay discarded.
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

// sendBackupAlarmMail selects the msmtp account-file transport explicitly.
// An environment API key must not switch delivery to an HTTP API or replace
// the configured account-file credential source.
func sendBackupAlarmMail(ctx context.Context, cfg *config.Config, message mailer.Message) error {
	sender := mailer.New(mailer.Config{
		Transport:   mailer.MethodSendmail,
		MsmtprcPath: cfg.BackupAlarmMsmtprcPath,
	})
	if err := sender.Send(ctx, message); err != nil {
		wrapped := fmt.Errorf("mail backup staleness alarm to %s: %w", message.To, err)
		// Transport failures include the account-file path here. The caller
		// separately logs the fault metrics and delivery outcome.
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

// backupAlarmHost returns a nonblank hostname or the unknown-host label.
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
