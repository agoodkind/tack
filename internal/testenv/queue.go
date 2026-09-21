package testenv

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

const (
	// queueService is the stack file service the brokers take their image from.
	queueService = "kafka"
	// queueBrokerCount is the size of the test cluster. Three brokers give a
	// topic three copies with a two-copy write minimum (TACK-409).
	queueBrokerCount = 3
	// queueClientPort is the port producers and consumers connect to.
	queueClientPort = "9092"
	// queueControllerPort is the port the KRaft quorum speaks on.
	queueControllerPort = "9093"
	// queueProbeTimeout bounds one readiness probe.
	queueProbeTimeout = 10 * time.Second
	// queueStopSeconds is the grace a stopped broker gets. A broker outage
	// needs no clean shutdown.
	queueStopSeconds = 0
	// queueClusterIDBytes is the size of a Kafka cluster id.
	queueClusterIDBytes = 16
	// queueHolderSeconds outlasts any test binary.
	queueHolderSeconds = "2147483647"
)

// queueBrokerContainers lists each broker's container by broker index.
// provisionQueue fills it inside the once that guards queueState.
var queueBrokerContainers []string

// provisionQueue starts the cluster and returns its bootstrap list once every
// broker answers.
func provisionQueue(ctx context.Context) (string, error) {
	image, err := serviceImage(ctx, queueService)
	if err != nil {
		return "", err
	}
	clusterID, err := queueClusterID(ctx)
	if err != nil {
		return "", err
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	holders, err := startQueueHolders(ctx, cli, image)
	if err != nil {
		return "", err
	}
	voters := queueVoters(holders)
	bootstrap := make([]string, 0, queueBrokerCount)
	started := make([]string, 0, queueBrokerCount)
	for index, holder := range holders {
		broker, err := startEngine(ctx, cli, engineSpec{
			kind:      "kafka-" + strconv.Itoa(index+1),
			image:     image,
			platform:  nil,
			cmd:       nil,
			env:       queueBrokerEnv(index, holder, voters, clusterID),
			networkOf: holder,
		})
		if err != nil {
			return "", err
		}
		started = append(started, broker.name)
		bootstrap = append(bootstrap, holder+":"+queueClientPort)
	}
	queueBrokerContainers = started
	list := strings.Join(bootstrap, ",")
	if err := waitForQueue(ctx, list); err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "testenv.queue.ready", slog.String("brokers", list))
	return list, nil
}

// startQueueHolders starts one sleeping container per broker, and each broker
// shares its holder's network stack. Container names are then fixed before any
// broker starts, which the static quorum requires, and a name still resolves
// to the same address across a broker restart.
func startQueueHolders(ctx context.Context, cli *client.Client, image string) ([]string, error) {
	holders := make([]string, 0, queueBrokerCount)
	for index := range queueBrokerCount {
		started, err := startEngine(ctx, cli, engineSpec{
			kind:       "kafka-address-" + strconv.Itoa(index+1),
			image:      image,
			platform:   nil,
			entrypoint: []string{"sleep"},
			cmd:        []string{queueHolderSeconds},
			env:        nil,
		})
		if err != nil {
			return nil, err
		}
		holders = append(holders, started.name)
	}
	return holders, nil
}

// queueVoters is the static KRaft quorum every broker starts with: each node
// id joined to the name its address holder answers to.
func queueVoters(holders []string) string {
	voters := make([]string, 0, len(holders))
	for index, holder := range holders {
		voters = append(voters, strconv.Itoa(index+1)+"@"+holder+":"+queueControllerPort)
	}
	return strings.Join(voters, ",")
}

// queueBrokerEnv is one broker's configuration. The listeners bind IPv4,
// because ensureNetwork creates an IPv4-only bridge while production runs
// IPv6 only. The internal-topic replication settings are omitted: Kafka's own
// defaults replicate those topics three ways.
func queueBrokerEnv(index int, holder, voters, clusterID string) []string {
	node := strconv.Itoa(index + 1)
	return []string{
		"KAFKA_PROCESS_ROLES=broker,controller",
		"KAFKA_NODE_ID=" + node,
		"KAFKA_CONTROLLER_QUORUM_VOTERS=" + voters,
		"KAFKA_LISTENERS=PLAINTEXT://0.0.0.0:" + queueClientPort + ",CONTROLLER://0.0.0.0:" + queueControllerPort,
		"KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://" + holder + ":" + queueClientPort,
		"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
		"KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT",
		"KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER",
		"CLUSTER_ID=" + clusterID,
	}
}

// queueClusterID generates the id every broker formats its metadata with. The
// engine reads 16 bytes of unpadded base64, the form kafka-storage
// random-uuid prints.
func queueClusterID(ctx context.Context) (string, error) {
	buffer := make([]byte, queueClusterIDBytes)
	if _, err := rand.Read(buffer); err != nil {
		slog.ErrorContext(ctx, "testenv.queue.random_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
