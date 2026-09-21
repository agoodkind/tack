package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// The two `ops queue` commands that take a copy count. They run in order: the
// topic gains its copies first, and the acks minimum rises afterwards. A topic
// holding fewer copies than its minimum rejects every acks=all write with
// NOT_ENOUGH_REPLICAS (TACK-409).

// queueReplicationInput is a topic, the copy count to move it onto, and the
// bytes per second each broker may spend on the move.
type queueReplicationInput struct {
	clispec.InputMarker
	Topic         string
	Replicas      int
	ThrottleBytes int
}

// queueMinInsyncInput is a topic with the in-sync copy count an acknowledged
// write requires.
type queueMinInsyncInput struct {
	clispec.InputMarker
	Topic    string
	Replicas int
}

// queueSetReplicationOp builds the command that raises a topic's copy count.
func queueSetReplicationOp(f *cli.Factory) clispec.Operation[queueReplicationInput] {
	return clispec.Operation[queueReplicationInput]{
		Name:     clispec.Name{Canonical: "set-replication", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsQueueSetReplication), Mutates: true},
		Group:    queueGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Move every partition of a topic onto the given number of brokers",
		Long: "Kafka fixes a topic's copy count when the topic is created, and a " +
			"partition reassignment is the only way to change it afterwards. " +
			"The command reads the live broker ids, plans a placement that " +
			"spreads leadership evenly, and hands the controller one plan " +
			"covering every partition. The copying runs in the background. " +
			"--throttle-bytes caps what each broker spends on it per second.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[queueReplicationInput]{
			clispec.StringParam("topic", queueTopicFlagHelp, "", false,
				func(in *queueReplicationInput, value string) { in.Topic = value }),
			clispec.IntParam("replicas", "how many brokers hold each partition", 0,
				func(in *queueReplicationInput, value int) { in.Replicas = value }),
			clispec.IntParam("throttle-bytes", "bytes per second per broker during the move, or 0 for no cap", 0,
				func(in *queueReplicationInput, value int) { in.ThrottleBytes = value }),
		},
		New: func() queueReplicationInput {
			return queueReplicationInput{
				InputMarker: clispec.InputMarker{}, Topic: "", Replicas: 0, ThrottleBytes: 0,
			}
		},
		Run: func(ctx context.Context, in queueReplicationInput, sink clispec.ResultSink) error {
			return RunQueueSetReplication(ctx, f.Cfg, queueTopicOrDefault(f.Cfg, in.Topic),
				in.Replicas, int64(in.ThrottleBytes), sink)
		},
	}
}

// queueSetMinInsyncOp builds the command that raises the acks requirement once
// the copies are in place.
func queueSetMinInsyncOp(f *cli.Factory) clispec.Operation[queueMinInsyncInput] {
	return clispec.Operation[queueMinInsyncInput]{
		Name:     clispec.Name{Canonical: "set-min-insync", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsQueueSetMinInsync), Mutates: true},
		Group:    queueGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Set how many in-sync copies an acknowledged write requires",
		Long: "Two of three copies is the value criterion 10 names: a write " +
			"survives the loss of one data guest, and the queue keeps serving " +
			"through that loss. The command refuses a value above the smallest " +
			"replica count across the topic's partitions, and the error names " +
			"both numbers.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[queueMinInsyncInput]{
			clispec.StringParam("topic", queueTopicFlagHelp, "", false,
				func(in *queueMinInsyncInput, value string) { in.Topic = value }),
			clispec.IntParam("replicas", "how many in-sync copies each acknowledged write needs", 0,
				func(in *queueMinInsyncInput, value int) { in.Replicas = value }),
		},
		New: func() queueMinInsyncInput {
			return queueMinInsyncInput{InputMarker: clispec.InputMarker{}, Topic: "", Replicas: 0}
		},
		Run: func(ctx context.Context, in queueMinInsyncInput, sink clispec.ResultSink) error {
			return RunQueueSetMinInsync(ctx, f.Cfg, queueTopicOrDefault(f.Cfg, in.Topic),
				in.Replicas, sink)
		},
	}
}
