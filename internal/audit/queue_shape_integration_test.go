package audit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/testenv"
)

const (
	// queueShapeRetention is the retention the shape declares. The assertions
	// below judge the two replication counts rather than this value.
	queueShapeRetention = 72 * time.Hour
	// queueShapeReplicas and queueShapeMinInSync are what acceptance criterion
	// 10 requires of the audit topic: three copies, two of them in sync before
	// a write commits (TACK-409).
	queueShapeReplicas  = 3
	queueShapeMinInSync = 2
	// queueShapeRunSize is the length of one counted produce run.
	queueShapeRunSize = 24
	// queueShapeProduceTimeout covers the leader election that follows the
	// loss of a broker leading a partition.
	queueShapeProduceTimeout = 60 * time.Second
	// queueShapeConsumeTimeout bounds the read-back of a produced run.
	queueShapeConsumeTimeout = 2 * time.Minute
)

// queueShape is the topic shape both tests ensure.
func queueShape() auditTopicShape {
	return auditTopicShape{
		Retention:         queueShapeRetention,
		ReplicationFactor: queueShapeReplicas,
		MinInSyncReplicas: queueShapeMinInSync,
	}
}

// TestEnsureAuditTopicReplicatesAcrossThreeBrokers reads the shape back from
// the cluster's own metadata after ensureAuditTopic writes it. The second
// round exercises the already-exists path against the topic the first round
// created.
func TestEnsureAuditTopicReplicatesAcrossThreeBrokers(t *testing.T) {
	brokers := SplitBrokers(testenv.Queue(t))
	admin := newQueueShapeClient(t, brokers)
	topic := "audit.events.shape." + uuid.NewString()
	for round := range 2 {
		if err := ensureAuditTopic(context.Background(), admin, topic, queueShape()); err != nil {
			t.Fatalf("ensureAuditTopic round %d: %v", round+1, err)
		}
		assertQueueShapeReplicas(t, admin, topic)
		assertQueueShapeMinInSync(t, admin, topic)
	}
}

// TestKafkaRecorderCommitsThroughOneBrokerLoss is acceptance criterion 10.
// The production recorder produces one counted run against the whole cluster
// and a second against two brokers of three. Every Record must return nil,
// and the topic must hold each produced event once.
func TestKafkaRecorderCommitsThroughOneBrokerLoss(t *testing.T) {
	brokers := SplitBrokers(testenv.Queue(t))
	admin := newQueueShapeClient(t, brokers)
	topic := "audit.events.outage." + uuid.NewString()
	if err := ensureAuditTopic(context.Background(), admin, topic, queueShape()); err != nil {
		t.Fatalf("ensureAuditTopic: %v", err)
	}
	recorder, err := NewKafkaRecorder(KafkaConfig{
		Brokers:        brokers,
		Topic:          topic,
		ClientID:       "tack-audit-outage-test",
		ProduceTimeout: queueShapeProduceTimeout,
	})
	if err != nil {
		t.Fatalf("NewKafkaRecorder: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })

	produced := produceQueueShapeRun(t, recorder, "whole")
	testenv.StopQueueBroker(t, 0)
	produced = append(produced, produceQueueShapeRun(t, recorder, "degraded")...)
	testenv.StartQueueBroker(t, 0)
	assertQueueShapeDelivered(t, brokers, topic, produced)
}
