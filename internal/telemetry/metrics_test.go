package telemetry

import "testing"

func TestAuditPartitionMetrics(t *testing.T) {
	SetAuditPartitionHeadroomWeeks(9)
	if got := auditPartitionHeadroomWeeks.Value(); got != 9 {
		t.Fatalf("headroom gauge = %d, want 9", got)
	}
	SetAuditPartitionHeadroomWeeks(3)
	if got := auditPartitionHeadroomWeeks.Value(); got != 3 {
		t.Fatalf("headroom gauge after update = %d, want 3", got)
	}
	IncAuditPartitionMaintenance("ok")
	IncAuditPartitionMaintenance("ok")
	IncAuditPartitionMaintenance("error")
	if got := auditPartitionMaintenanceTotal.Get("ok").String(); got != "2" {
		t.Fatalf("maintenance ok count = %s, want 2", got)
	}
	if got := auditPartitionMaintenanceTotal.Get("error").String(); got != "1" {
		t.Fatalf("maintenance error count = %s, want 1", got)
	}
}
