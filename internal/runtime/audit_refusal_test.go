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

// TestBuildAuditRecorderRefusesUnreachableBrokers checks that startup rejects
// unavailable Kafka brokers after a real readiness check. Client construction
// alone does not connect to a broker (TACK-439).
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
		t.Fatal("startup must reject brokers that cannot accept connections")
	}

	if recorder != nil {
		t.Fatalf("recorder = %T, want nil after backend setup failure", recorder)
	}

	if !strings.Contains(err.Error(), "refusing to serve unrecorded") {
		t.Fatalf("err = %v, want it to name why the server refuses", err)
	}
}

// TestBuildAuditRecorderRefusesUnreachableWriter checks startup rejection
// when the synchronous Yugabyte writer cannot connect.
func TestBuildAuditRecorderRefusesUnreachableWriter(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		AuditWriterDSN: "postgres://tack@127.0.0.1:1/tack?sslmode=disable&connect_timeout=2",
	}

	recorder, err := buildAuditRecorder(context.Background(), cfg)
	if err == nil {
		t.Fatal("startup must reject an unavailable audit writer")
	}

	if recorder != nil {
		t.Fatalf("recorder = %T, want nil", recorder)
	}
}

// TestBuildAuditRecorderRefusesUnconfigured checks that startup rejects
// missing audit configuration instead of serving unaudited requests.
func TestBuildAuditRecorderRefusesUnconfigured(t *testing.T) {
	t.Parallel()

	recorder, err := buildAuditRecorder(context.Background(), &config.Config{})
	if err == nil {
		t.Fatal("startup must reject missing audit backend configuration")
	}

	if recorder != nil {
		t.Fatalf("recorder = %T, want nil", recorder)
	}

	if !strings.Contains(err.Error(), "AUDIT_ALLOW_UNRECORDED") {
		t.Fatalf("err = %v, want it to name the flag that permits unrecorded operation", err)
	}
}

// TestBuildAuditRecorderAllowsAcknowledgedUnrecorded checks that an explicit
// operator setting permits a deployment without audit recording.
func TestBuildAuditRecorderAllowsAcknowledgedUnrecorded(t *testing.T) {
	t.Parallel()

	recorder, err := buildAuditRecorder(
		context.Background(),
		&config.Config{AuditAllowUnrecorded: true},
	)
	if err != nil {
		t.Fatalf("an acknowledged unrecorded deployment must start: %v", err)
	}

	if _, ok := recorder.(audit.NoopRecorder); !ok {
		t.Fatalf("recorder = %T, want the discarding recorder the operator asked for", recorder)
	}
}

// TestUnwiredRecorderRefusesInsteadOfDiscarding checks that a boundary without
// an installed recorder returns an error instead of discarding evidence.
func TestUnwiredRecorderRefusesInsteadOfDiscarding(t *testing.T) {
	t.Parallel()

	err := audit.UnwiredRecorder{}.Record(context.Background(), audit.Event{Verb: "node.read"})
	if err == nil {
		t.Fatal("an uninstalled sink must refuse, not silently discard")
	}
}

// TestBuildKafkaRecorderWaitsForASlowBroker checks startup retries while
// a broker starts alongside the app.
func TestBuildKafkaRecorderWaitsForASlowBroker(t *testing.T) {
	t.Parallel()
	// Reserve the port until immediately before broker startup to prevent
	// another process from allocating it during the delay.
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}

	address := reservation.Addr().String()
	port := portOf(t, address)
	started := make(chan *kfake.Cluster, 1)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if closeErr := reservation.Close(); closeErr != nil {
			started <- nil
			return
		}

		cluster, err := kfake.NewCluster(
			kfake.NumBrokers(1),
			kfake.Ports(port),
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

	recorder, err := buildAuditRecorder(
		context.Background(),
		&config.Config{
			AuditKafkaBrokers:        address,
			AuditKafkaTopic:          "audit.events.v1",
			AuditKafkaProduceTimeout: 2 * time.Second,
			AuditBrokerReadyTimeout:  20 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("a broker that becomes ready must not refuse the server: %v", err)
	}

	if closer, ok := recorder.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
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

// TestBuildAuditRecorderRefusesAMissingTopic checks that a connected broker
// without the required audit topic cannot pass startup readiness.
func TestBuildAuditRecorderRefusesAMissingTopic(t *testing.T) {
	t.Parallel()
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1))
	if err != nil {
		t.Fatalf("kfake.NewCluster: %v", err)
	}

	t.Cleanup(cluster.Close)

	// Allow metadata discovery to finish before the startup timeout.
	// This test checks a missing-topic error rather than a deadline error.
	recorder, err := buildAuditRecorder(
		context.Background(),
		&config.Config{
			AuditKafkaBrokers:        strings.Join(cluster.ListenAddrs(), ","),
			AuditKafkaTopic:          "audit.events.v1",
			AuditKafkaProduceTimeout: time.Second,
			AuditBrokerReadyTimeout:  20 * time.Second,
		},
	)
	if err == nil {
		t.Fatal("a reachable broker with no audit topic must still refuse")
	}

	if recorder != nil {
		t.Fatalf("recorder = %T, want nil", recorder)
	}

	if !strings.Contains(err.Error(), "audit.events.v1") {
		t.Fatalf("err = %v, want it to name the topic that cannot be written", err)
	}
}
