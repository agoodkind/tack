package runtime

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// TestBuildAuditRecorderRefusesUnreachableBrokers is TACK-439's core case: a
// server whose ledger backend is unreachable used to start anyway and record
// nothing, and a caller could not tell. The producer connects lazily, so the
// refusal has to come from a real reachability check rather than from
// construction.
func TestBuildAuditRecorderRefusesUnreachableBrokers(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		AuditKafkaBrokers:        "127.0.0.1:1",
		AuditKafkaTopic:          "audit.events.v1",
		AuditKafkaProduceTimeout: time.Second,
		AuditBrokerReadyTimeout:  2 * time.Second,
	}

	recorder, err := buildAuditRecorder(context.Background(), cfg)

	if err == nil {
		t.Fatal("a server that cannot reach its brokers must refuse to start")
	}
	if recorder != nil {
		t.Fatalf("recorder = %T, want none: a failed backend must never yield a sink", recorder)
	}
	if !strings.Contains(err.Error(), "refusing to serve unrecorded") {
		t.Fatalf("err = %v, want it to name why the server refuses", err)
	}
}

// TestBuildAuditRecorderRefusesUnreachableWriter covers the same failure on
// the Yugabyte path, which is what a deployment without Kafka runs.
func TestBuildAuditRecorderRefusesUnreachableWriter(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		AuditWriterDSN: "postgres://tack@127.0.0.1:1/tack?sslmode=disable&connect_timeout=2",
	}

	recorder, err := buildAuditRecorder(context.Background(), cfg)

	if err == nil {
		t.Fatal("an unreachable audit writer must refuse to start")
	}
	if recorder != nil {
		t.Fatalf("recorder = %T, want none", recorder)
	}
}

// TestBuildAuditRecorderRefusesUnconfigured pins that an absent backend is a
// refusal rather than a warning. The previous behavior logged one line at
// startup and served every request unrecorded afterwards.
func TestBuildAuditRecorderRefusesUnconfigured(t *testing.T) {
	t.Parallel()

	recorder, err := buildAuditRecorder(context.Background(), &config.Config{})

	if err == nil {
		t.Fatal("no configured backend must refuse to start")
	}
	if recorder != nil {
		t.Fatalf("recorder = %T, want none", recorder)
	}
	if !strings.Contains(err.Error(), "AUDIT_ALLOW_UNRECORDED") {
		t.Fatalf("err = %v, want it to name the flag that permits unrecorded operation", err)
	}
}

// TestBuildAuditRecorderAllowsAcknowledgedUnrecorded pins the one path to a
// discarding recorder: the operator declaring it. Without this the change
// would make a deliberately unaudited local environment unstartable.
func TestBuildAuditRecorderAllowsAcknowledgedUnrecorded(t *testing.T) {
	t.Parallel()

	recorder, err := buildAuditRecorder(context.Background(), &config.Config{AuditAllowUnrecorded: true})
	if err != nil {
		t.Fatalf("an acknowledged unrecorded deployment must start: %v", err)
	}
	if _, ok := recorder.(audit.NoopRecorder); !ok {
		t.Fatalf("recorder = %T, want the discarding recorder the operator asked for", recorder)
	}
}

// TestUnwiredRecorderRefusesInsteadOfDiscarding pins the second half of the
// defect: both request boundaries defaulted to a recorder that dropped events
// and returned success, so a missed installation was indistinguishable from a
// working ledger.
func TestUnwiredRecorderRefusesInsteadOfDiscarding(t *testing.T) {
	t.Parallel()

	err := audit.UnwiredRecorder{}.Record(context.Background(), audit.Event{Verb: "node.read"})

	if err == nil {
		t.Fatal("an uninstalled sink must refuse, not silently discard")
	}
}

// TestBuildKafkaRecorderWaitsForASlowBroker pins that the refusal does not
// punish a cold start. The broker here answers only after the first attempt
// would have failed, which is what compose does when it brings Kafka up
// alongside the app; refusing on the first attempt would crash-loop a healthy
// deployment.
func TestBuildKafkaRecorderWaitsForASlowBroker(t *testing.T) {
	t.Parallel()
	address := reservedLocalAddress(t)
	started := make(chan *kfake.Cluster, 1)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		cluster, err := kfake.NewCluster(
			kfake.NumBrokers(1),
			kfake.Ports(portOf(t, address)),
			kfake.SeedTopics(1, "audit.events.v1"),
		)
		if err != nil {
			started <- nil
			return
		}
		started <- cluster
	}()
	t.Cleanup(func() {
		if cluster := <-started; cluster != nil {
			cluster.Close()
		}
	})

	recorder, err := buildAuditRecorder(context.Background(), &config.Config{
		AuditKafkaBrokers:        address,
		AuditKafkaTopic:          "audit.events.v1",
		AuditKafkaProduceTimeout: 2 * time.Second,
		AuditBrokerReadyTimeout:  20 * time.Second,
	})
	if err != nil {
		t.Fatalf("a broker that becomes ready must not refuse the server: %v", err)
	}
	if closer, ok := recorder.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

// reservedLocalAddress returns a loopback address that nothing is listening on
// yet, so the first reachability attempt fails.
func reservedLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return address
}

func portOf(t *testing.T, address string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split %q: %v", address, err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("port %q: %v", port, err)
	}
	return number
}

// TestBuildAuditRecorderRefusesAMissingTopic answers the review finding that a
// broker answering is not the same as a ledger that can be written. This
// cluster is up and healthy at the connection level and has no audit topic, so
// every Record would fail while the old check passed.
func TestBuildAuditRecorderRefusesAMissingTopic(t *testing.T) {
	t.Parallel()
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1))
	if err != nil {
		t.Fatalf("kfake.NewCluster: %v", err)
	}
	t.Cleanup(cluster.Close)

	recorder, err := buildAuditRecorder(context.Background(), &config.Config{
		AuditKafkaBrokers:        strings.Join(cluster.ListenAddrs(), ","),
		AuditKafkaTopic:          "audit.events.v1",
		AuditKafkaProduceTimeout: time.Second,
		AuditBrokerReadyTimeout:  2 * time.Second,
	})

	if err == nil {
		t.Fatal("a reachable broker with no audit topic must still refuse")
	}
	if recorder != nil {
		t.Fatalf("recorder = %T, want none", recorder)
	}
	if !strings.Contains(err.Error(), "audit.events.v1") {
		t.Fatalf("err = %v, want it to name the topic that cannot be written", err)
	}
}
