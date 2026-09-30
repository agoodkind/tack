package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

type actualSearchServer struct {
	command  *exec.Cmd
	endpoint string
	done     chan error
}

func buildActualSearchServer(t *testing.T) string {
	t.Helper()
	revision := actualServerRevision(t)
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "tack-server")
	build := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-tags=fdb", "-ldflags", "-X goodkind.io/tack/internal/version.commit="+revision+" -X goodkind.io/tack/internal/version.dirty=true", "-o", binary, "./cmd/server")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual server: %v: %s", err, output)
	}
	file, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	t.Logf("actual cmd/server revision=%s dirty=true binary_sha256=%s", revision, hex.EncodeToString(digest.Sum(nil)))
	return binary
}

func actualServerRevision(t *testing.T) string {
	t.Helper()
	revision := strings.TrimSpace(os.Getenv("TACK_TEST_SOURCE_REVISION"))
	if len(revision) != 40 {
		t.Fatal("TACK_TEST_SOURCE_REVISION must contain the host checkout commit")
	}
	if _, err := hex.DecodeString(revision); err != nil {
		t.Fatal("TACK_TEST_SOURCE_REVISION must contain a hexadecimal commit")
	}
	return revision
}

func startActualSearchServer(t *testing.T, binary string, cfg *config.Config) *actualSearchServer {
	t.Helper()
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(t.TempDir(), "server.log"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "serve", "--operator-service", "tack-search-acceptance", "--execute")
	command.Stdout, command.Stderr = logFile, logFile
	command.Env = actualServerEnvironment(cfg, port)
	t.Cleanup(func() {
		if t.Failed() {
			recordActualServerFailure(t, logFile.Name(), command.Env)
		}
	})
	if err := command.Start(); err != nil {
		logFile.Close()
		t.Fatalf("start actual server: %v", err)
	}
	t.Logf("actual server started pid=%d endpoint=http://127.0.0.1:%d", command.Process.Pid, port)
	server := &actualSearchServer{command: command, endpoint: "http://127.0.0.1:" + strconv.Itoa(port), done: make(chan error, 1)}
	go func() { server.done <- command.Wait(); logFile.Close() }()
	t.Cleanup(func() { server.stop(t) })
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	for {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.endpoint+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				t.Logf("actual server pid=%d endpoint=%s", command.Process.Pid, server.endpoint)
				return server
			}
		}
		select {
		case err := <-server.done:
			server.command = nil
			t.Fatalf("actual server exited before health readiness: %v", err)
		case <-deadline.C:
			t.Fatal("actual server health readiness timed out")
		case <-t.Context().Done():
			t.Fatal("actual server readiness canceled")
		case <-ticker.C:
		}
	}
}

func (server *actualSearchServer) stop(t *testing.T) {
	t.Helper()
	if server.command == nil {
		return
	}
	pid := server.command.Process.Pid
	if err := server.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("stop actual server pid=%d: %v", pid, err)
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	select {
	case err := <-server.done:
		if err != nil {
			t.Errorf("actual server pid=%d exit: %v", pid, err)
		}
	case <-deadline.C:
		server.command.Process.Kill()
		<-server.done
		t.Errorf("actual server pid=%d required forced termination", pid)
	}
	server.command = nil
}

func actualServerEnvironment(cfg *config.Config, port int) []string {
	overrides := map[string]string{
		"DATABASE_URL": cfg.DatabaseURL, "FDB_CLUSTER_FILE": cfg.FDBClusterFile,
		"PORT": strconv.Itoa(port), "AUDIT_WRITER_DSN": cfg.DatabaseURL,
		"AUDIT_OPERATOR_DSN":  cfg.DatabaseURL,
		"AUDIT_KAFKA_BROKERS": "", "AUDIT_ALLOW_UNRECORDED": "false",
		"AUDIT_READ_FLUSH_INTERVAL": "10ms", "LOG_JSON_FILE": "-", "LOG_TEXT_FILE": "-",
		"AUTH_TOKEN_CACHE_LIFETIME": "0s", "AUTH_MEMBERSHIP_CACHE_LIFETIME": "0s",
		"TACK_DATAGEN_ALLOW_TARGET": "local",
	}
	environment := make([]string, 0)
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		if _, overridden := overrides[key]; !overridden {
			environment = append(environment, variable)
		}
	}
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}
