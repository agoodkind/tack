package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// queueGroup is the parent of the commands that read and change the audit
// event queue's cluster shape. Together they are the online migration from one
// broker to one per data guest: read what the brokers report, raise the copy
// count on each topic, watch the move finish, then raise the acks minimum
// (TACK-409).
var queueGroup = &clispec.Group{
	Use: "queue", Short: "Read and change the audit event queue's cluster shape",
	Long: "", Parent: opsGroup,
}

// queueTopicInput is one topic name.
type queueTopicInput struct {
	clispec.InputMarker
	Topic string
}

// queueTopicOrDefault falls back to the configured audit topic when the
// operator names none.
func queueTopicOrDefault(cfg *config.Config, topic string) string {
	if topic != "" {
		return topic
	}
	return cfg.AuditKafkaTopic
}

// queueTopicFlagHelp is the one-line help for every --topic flag here.
const queueTopicFlagHelp = "topic name (default: this environment's AUDIT_KAFKA_TOPIC)"

// queueTopicParams declares --topic as the only flag of a command.
func queueTopicParams() []clispec.Param[queueTopicInput] {
	return []clispec.Param[queueTopicInput]{
		clispec.StringParam("topic", queueTopicFlagHelp, "", false,
			func(in *queueTopicInput, value string) { in.Topic = value }),
	}
}

// newQueueTopicInput builds the empty input those commands parse into.
func newQueueTopicInput() queueTopicInput {
	return queueTopicInput{InputMarker: clispec.InputMarker{}, Topic: ""}
}

// queueStatusOp declares the `ops queue status` leaf.
func queueStatusOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "status", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsQueueStatus), Reads: true},
		Group:    queueGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Print what the audit queue's brokers report about themselves",
		Long: "Prints every broker id with the address that broker advertises, " +
			"the controller quorum leader and its voters, and the partition, " +
			"copy, and in-sync counts of the audit topic and the " +
			"consumer-position topic. Criterion 10 is read from this output " +
			"rather than from what a deploy intended.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return RunQueueStatus(ctx, f.Cfg, sink)
		},
	}
}

// queueReplicationProgressOp builds the command an operator polls while a
// copy-count change runs.
func queueReplicationProgressOp(f *cli.Factory) clispec.Operation[queueTopicInput] {
	return clispec.Operation[queueTopicInput]{
		Name:     clispec.Name{Canonical: "replication-progress", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsQueueReplicationProgress), Reads: true},
		Group:    queueGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Report how much of a copy-count change is left to run",
		Long: "A reassignment copies whole partitions between brokers and runs " +
			"in the background. This counts the partitions the controller is " +
			"still moving and the partitions already at the target copy count.",
		Examples: nil,
		Args:     nil,
		Params:   queueTopicParams(),
		New:      newQueueTopicInput,
		Run: func(ctx context.Context, in queueTopicInput, sink clispec.ResultSink) error {
			return RunQueueReplicationProgress(ctx, f.Cfg, queueTopicOrDefault(f.Cfg, in.Topic), sink)
		},
	}
}

// queueClearThrottleOp builds the last step of a copy-count change.
func queueClearThrottleOp(f *cli.Factory) clispec.Operation[queueTopicInput] {
	return clispec.Operation[queueTopicInput]{
		Name:     clispec.Name{Canonical: "clear-throttle", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsQueueClearThrottle), Mutates: true},
		Group:    queueGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Remove the replication rate caps a copy-count change set",
		Long: "Deletes the two per-broker rate keys and the two per-topic replica " +
			"lists. A cap left in place after the move keeps slowing the " +
			"recovery of any replica that falls behind later.",
		Examples: nil,
		Args:     nil,
		Params:   queueTopicParams(),
		New:      newQueueTopicInput,
		Run: func(ctx context.Context, in queueTopicInput, sink clispec.ResultSink) error {
			return RunQueueClearThrottle(ctx, f.Cfg, queueTopicOrDefault(f.Cfg, in.Topic), sink)
		},
	}
}
