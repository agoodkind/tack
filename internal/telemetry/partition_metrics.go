package telemetry

import "expvar"

var auditPartitionStrayChildren = expvar.NewInt("tack_audit_partition_stray_children")

// SetAuditPartitionStrayChildren publishes the count of children of
// audit.events named outside events_pYYYY_MM_DD.
func SetAuditPartitionStrayChildren(count int64) { auditPartitionStrayChildren.Set(count) }
