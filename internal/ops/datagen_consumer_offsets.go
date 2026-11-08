package ops

import (
	"context"
	"fmt"
	"net/url"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

func readDatagenConsumerOffsets(ctx context.Context, factory *cli.Factory) (auditConsumerOffsetsReport, error) {
	selection := datagenReportSelection{Report: "ops.audit.consumer_offsets", Topic: factory.Cfg.AuditKafkaTopic}
	return recordDatagenReport(ctx, factory, readSpec(audit.VerbOpsAuditConsumerOffsets, "audit/consumer-offsets"), selection,
		func(ctx context.Context) (auditConsumerOffsetsReport, []audit.Target, error) {
			report, err := readAuditConsumerOffsets(ctx, factory)
			targets := make([]audit.Target, 0, len(report.Partitions)+2)
			for _, partition := range report.Partitions {
				reference := fmt.Sprintf("audit/consumer-offsets/%s/%s/%d",
					url.PathEscape(report.Topic), url.PathEscape(partition.Group), partition.Partition)
				targets = append(targets, externalTarget(reference))
			}
			targets = append(targets, externalTarget("audit/operator-outbox"), externalTarget("audit/foundationdb-outbox"))
			return report, targets, err
		})
}
