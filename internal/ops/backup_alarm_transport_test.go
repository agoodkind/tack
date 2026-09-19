package ops

import (
	"context"
	"net"
	"strings"
	"testing"

	"goodkind.io/send-email/mailer"

	"goodkind.io/tack/internal/config"
)

// backupAlarmNetworkFooterLabels are the rows the mail library's HTML footer
// prints. Each names a value that says where this guest sits on the network:
// the address the internet sees it at, the company that sells it the line, and
// one row per interface address. An alarm read on a phone needs none of them,
// and a mailbox is not where a host's addresses belong. The ISP's name and the
// public address are only ever printed under these labels, so a message that
// carries no label carries neither value.
var backupAlarmNetworkFooterLabels = []string{"Public IPv4", "Public IPv6", "ISP"}

// hostNetworkAddresses is every address on this guest's own interfaces, the
// strings the library's footer printed one per row. Loopback and link-local
// addresses are left out: they name no guest.
func hostNetworkAddresses(t *testing.T) []string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("read interfaces: %v", err)
	}
	var addresses []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		list, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, entry := range list {
			ip := interfaceAddressIP(entry)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			addresses = append(addresses, ip.String())
		}
	}
	return addresses
}

func interfaceAddressIP(entry net.Addr) net.IP {
	switch value := entry.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}

// sendOneAlarmThroughSMTP sends one alarm message through the production
// transport and returns the bytes the relay received.
func sendOneAlarmThroughSMTP(t *testing.T, message mailer.Message) string {
	t.Helper()
	server := startAlarmSMTPServer(t)
	cfg := &config.Config{BackupAlarmMsmtprcPath: writeAlarmMsmtprc(t, server.port(t))}
	if err := sendBackupAlarmMail(context.Background(), cfg, message); err != nil {
		t.Fatalf("send the alarm: %v", err)
	}
	select {
	case received := <-server.received:
		return received
	default:
		t.Fatal("the relay received no message")
		return ""
	}
}

// TestBackupAlarmMailCarriesNoHostAddresses sends one alarm to a relay on
// loopback and reads what arrived. Every part of the message must name the
// guest, the time and the fault, and no part may carry an address of any
// interface on this host, the address the internet sees it at, or the name of
// the company that sells it the line.
func TestBackupAlarmMailCarriesNoHostAddresses(t *testing.T) {
	message := mailer.Message{
		To:      "backups@example.test",
		Subject: "[tack-qa] Ledger cluster unhealthy for 32 minutes",
		Body: "The ledger cluster (logins and audit trail) was last healthy at 3:51 PM UTC on Sep 6, 2026, " +
			"32 minutes ago; the limit is 30 minutes.\n\n1. Confirm every ledger guest is up.",
		From:   "",
		Name:   "",
		Caller: backupAlarmCaller,
	}

	received := sendOneAlarmThroughSMTP(t, message)

	// A failure names the interface rather than the address, because a test
	// log that prints the leak to prove it is the same leak in another place.
	for index, address := range hostNetworkAddresses(t) {
		if strings.Contains(received, address) {
			t.Errorf("the message carries this guest's interface address number %d", index)
		}
	}
	for _, label := range backupAlarmNetworkFooterLabels {
		if strings.Contains(received, label) {
			t.Errorf("the message carries the network footer row %q", label)
		}
	}
}

// TestBackupAlarmMailKeepsTheHostAndTime proves the footer the operator does
// need survived: the message still says which guest sent it, which run
// produced it, and when. An alarm that names no guest is an alarm nobody can
// act on, so removing the addresses must not remove these.
func TestBackupAlarmMailKeepsTheHostAndTime(t *testing.T) {
	message := mailer.Message{
		To:      "backups@example.test",
		Subject: "[tack-qa] Restore rehearsal has not passed in 9 days",
		Body:    "The restore rehearsal (the daily test restore) last passed 9 days ago; the limit is 8 days.",
		From:    "",
		Name:    "",
		Caller:  backupAlarmCaller,
	}

	received := sendOneAlarmThroughSMTP(t, message)

	for _, want := range []string{
		message.Subject,
		"The restore rehearsal (the daily test restore) last passed 9 days ago",
		backupAlarmHost(),
		backupAlarmCaller,
		"To: backups@example.test",
	} {
		if !strings.Contains(received, want) {
			t.Errorf("the message is missing %q:\n%s", want, received)
		}
	}
	if !strings.Contains(received, "UTC") {
		t.Errorf("the message states no send time in UTC:\n%s", received)
	}
}
