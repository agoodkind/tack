package testenv

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"

	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	// kafkaImage is the broker image docker-compose.yml pins for the audit
	// profile.
	kafkaImage = "apache/kafka:4.2.0"
	// kafkaPort is the broker listener port.
	kafkaPort = "9092"
	// kafkaHolderSeconds is the sleep length of the address holder container,
	// longer than any test process.
	kafkaHolderSeconds = "2147483647"
	// kafkaClusterIDBytes is the size of the KRaft cluster ID before its
	// base64url encoding to 22 characters.
	kafkaClusterIDBytes = 16
)

var kafkaState provisioned

// Kafka returns the bootstrap address of this process's single-node Kafka
// broker in KRaft mode. Every test in the process shares the broker. Each test
// uses its own topic and consumer group.
func Kafka(t T) string {
	t.Helper()
	skipWhenShort(t)
	return kafkaState.get(t, provisionKafka)
}

// provisionKafka starts the broker and returns its bootstrap address once it
// answers a metadata request. The broker advertises its own address. A
// container that only sleeps owns that address before the broker starts, and
// the broker shares the sleeping container's network stack.
func provisionKafka(ctx context.Context) (string, error) {
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	holder, err := startEngine(ctx, cli, engineSpec{
		kind:       "kafka-address",
		image:      kafkaImage,
		platform:   nil,
		entrypoint: []string{"sleep"},
		cmd:        []string{kafkaHolderSeconds},
		env:        nil,
	})
	if err != nil {
		return "", err
	}
	clusterID, err := kafkaClusterID(ctx)
	if err != nil {
		return "", err
	}
	bootstrap := net.JoinHostPort(holder.address, kafkaPort)
	if _, err := startEngine(ctx, cli, engineSpec{
		kind:      "kafka",
		image:     kafkaImage,
		platform:  nil,
		cmd:       nil,
		env:       kafkaEnvironment(bootstrap, clusterID),
		networkOf: holder.name,
	}); err != nil {
		return "", err
	}
	if err := waitForKafka(ctx, bootstrap); err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "testenv.kafka.ready", slog.String("bootstrap", bootstrap))
	return bootstrap, nil
}

// kafkaEnvironment is the docker-compose.yml broker configuration with the
// test broker's advertised address, cluster ID, and a bounded heap.
func kafkaEnvironment(bootstrap, clusterID string) []string {
	return []string{
		"KAFKA_PROCESS_ROLES=broker,controller",
		"KAFKA_NODE_ID=1",
		"KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093",
		"KAFKA_LISTENERS=PLAINTEXT://[::]:" + kafkaPort + ",CONTROLLER://localhost:9093",
		"KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://" + bootstrap,
		"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
		"KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT",
		"KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER",
		"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1",
		"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1",
		"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1",
		"KAFKA_DEFAULT_REPLICATION_FACTOR=1",
		"KAFKA_MIN_INSYNC_REPLICAS=1",
		"KAFKA_UNCLEAN_LEADER_ELECTION_ENABLE=false",
		"KAFKA_HEAP_OPTS=-Xms256m -Xmx512m",
		"CLUSTER_ID=" + clusterID,
	}
}

// kafkaClusterID returns a new KRaft cluster ID in the form that
// kafka-storage.sh random-uuid prints.
func kafkaClusterID(ctx context.Context) (string, error) {
	raw := make([]byte, kafkaClusterIDBytes)
	if _, err := rand.Read(raw); err != nil {
		slog.ErrorContext(ctx, "testenv.kafka.cluster_id_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("generate the kafka cluster id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// waitForKafka pings the broker until it answers or ctx ends.
func waitForKafka(ctx context.Context, bootstrap string) error {
	client, err := kgo.NewClient(kgo.SeedBrokers(bootstrap))
	if err != nil {
		slog.ErrorContext(ctx, "testenv.kafka.client_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open a kafka client for %s: %w", bootstrap, err)
	}
	defer client.Close()
	for {
		lastErr := client.Ping(ctx)
		if lastErr == nil {
			return nil
		}
		slog.DebugContext(ctx, "testenv.kafka.probe", slog.String("err", lastErr.Error()))
		if !sleepOrDone(ctx) {
			slog.ErrorContext(ctx, "testenv.kafka.not_ready", slog.String("err", lastErr.Error()))
			return fmt.Errorf("the test kafka broker at %s answered no metadata request before the deadline: %w", bootstrap, lastErr)
		}
	}
}
