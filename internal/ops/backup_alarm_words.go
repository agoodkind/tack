package ops

import "goodkind.io/tack/internal/config"

// backupStalenessAlarmSubject identifies the check and the host.
func backupStalenessAlarmSubject(host string) string {
	return "[" + host + "] Backup check reported a problem"
}

// backupStalenessAlarmBody returns the check output without credentials.
func backupStalenessAlarmBody(cfg *config.Config, faults []backupStalenessMetric) string {
	return redactSecret(cfg, backupStalenessReport(faults))
}
