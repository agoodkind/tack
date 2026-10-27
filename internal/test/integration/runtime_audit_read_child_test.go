package integration_test

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

const (
	readChildActionVariable  = "TACK_READ_CHILD_ACTION"
	readChildBackendVariable = "TACK_READ_CHILD_BACKEND"
	readChildClusterVariable = "TACK_READ_CHILD_CLUSTER_FILE"
	readChildPrefixVariable  = "TACK_READ_CHILD_PREFIX"
	readChildDSNVariable     = "TACK_READ_CHILD_DSN"
	readChildCompletionFile  = "TACK_READ_CHILD_COMPLETION"
	readChildResultPrefix    = "runtime-audit-read-result "
	readChildReadyLine       = "runtime-audit-read-ready"
	readChildStartedLine     = "runtime-audit-read-started"
	readChildActionRecover   = "recover"
	readChildActionCleanup   = "cleanup"
	readChildActionFinalize  = "finalize"
	readChildOwnerLostResult = "owner_lost"
	readChildCompletedResult = "completed"
)

// TestReadLifecycleChildProcess runs one read lifecycle action in a separate
// operating system process for the runtime audit read tests. It skips unless
// a parent test starts it.
func TestReadLifecycleChildProcess(t *testing.T) {
	action := os.Getenv(readChildActionVariable)
	if action == "" {
		t.Skip("run the child action: parent action is missing")
	}
	store := openChildReadStore(t)
	fmt.Println(readChildReadyLine)
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatalf("wait for the start line: %v", err)
	}
	fmt.Println(readChildStartedLine)
	var result string
	switch action {
	case readChildActionRecover, readChildActionCleanup:
		total := 0
		for bucket := range uint16(audit.ReadRecoveryBuckets) {
			count, err := childBucketAction(t, store, action, bucket)
			if err != nil {
				t.Fatalf("%s bucket %d: %v", action, bucket, err)
			}
			total += count
		}
		result = strconv.Itoa(total)
	case readChildActionFinalize:
		result = childFinalize(t, store)
	default:
		t.Fatalf("child action = %q, want recover, cleanup, or finalize", action)
	}
	fmt.Println(readChildResultPrefix + result)
}

// childBucketAction runs recovery or cleanup in one bucket.
func childBucketAction(t *testing.T, store audit.ReadLifecycle, action string, bucket uint16) (int, error) {
	if action == readChildActionRecover {
		return store.RecoverExpired(t.Context(), bucket, audit.ReadRecoveryScanLimit)
	}
	return store.CleanupPublished(t.Context(), bucket, audit.ReadCleanupBatch)
}

// childFinalize finalizes the completion that the parent wrote to a file.
func childFinalize(t *testing.T, store audit.ReadLifecycle) string {
	encoded, err := os.ReadFile(os.Getenv(readChildCompletionFile))
	if err != nil {
		t.Fatalf("read the completion file: %v", err)
	}
	var completion childCompletion
	if err := json.Unmarshal(encoded, &completion); err != nil {
		t.Fatalf("decode the completion: %v", err)
	}
	err = store.FinalizeRead(t.Context(), completion.readCompletion())
	switch {
	case err == nil:
		return readChildCompletedResult
	case errors.Is(err, audit.ErrReadOwnerLost):
		return readChildOwnerLostResult
	default:
		t.Fatalf("finalize in the child process: %v", err)
		return ""
	}
}

// openChildReadStore opens the store that the parent selects through environment variables.
func openChildReadStore(t *testing.T) audit.ReadLifecycle {
	if os.Getenv(readChildBackendVariable) == "yugabytedb" {
		return audit.NewPoolReadLifecycle(applicationRolePool(t, os.Getenv(readChildDSNVariable)))
	}
	prefix, err := hex.DecodeString(os.Getenv(readChildPrefixVariable))
	if err != nil {
		t.Fatalf("decode the test prefix: %v", err)
	}
	fdbadapter.SetTestPrefix(prefix)
	stores, err := fdbadapter.NewStores(os.Getenv(readChildClusterVariable), 5*time.Second, nil)
	if err != nil {
		t.Fatalf("open FoundationDB stores: %v", err)
	}
	return stores.AuditReads(clock.Wall{})
}

// childProcess controls a child process that waits for a start line.
type childProcess struct {
	command *exec.Cmd
	start   io.WriteCloser
	output  *bufio.Scanner
	log     strings.Builder
}

// startReadChild starts TestReadLifecycleChildProcess with action and environment
// and waits until the child process opens its store.
func startReadChild(t *testing.T, action string, environment []string) *childProcess {
	t.Helper()
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestReadLifecycleChildProcess$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), append(environment, readChildActionVariable+"="+action)...)
	start, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("open child stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open child stdout: %v", err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatalf("start the child process: %v", err)
	}
	child := &childProcess{command: command, start: start, output: bufio.NewScanner(stdout), log: strings.Builder{}}
	if _, found := child.lineWithPrefix(readChildReadyLine); !found {
		t.Fatalf("wait for the child process store: child exited:\n%s", child.log.String())
	}
	return child
}

// signal sends the start line to the child process and waits until the
// child reports that its action started.
func (child *childProcess) signal(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(child.start, "start\n"); err != nil {
		t.Fatalf("send the start line: %v", err)
	}
	if _, found := child.lineWithPrefix(readChildStartedLine); !found {
		t.Fatalf("wait for the child action to start: child exited:\n%s", child.log.String())
	}
}

// result waits for the child process to exit and returns its result.
func (child *childProcess) result(t *testing.T) string {
	t.Helper()
	result, found := child.lineWithPrefix(readChildResultPrefix)
	if err := child.command.Wait(); err != nil || !found {
		t.Fatalf("read the child process result: %v\n%s", err, child.log.String())
	}
	return result
}

// lineWithPrefix reads output lines until one starts with prefix and returns
// the rest of that line.
func (child *childProcess) lineWithPrefix(prefix string) (string, bool) {
	for child.output.Scan() {
		line := child.output.Text()
		child.log.WriteString(line + "\n")
		if rest, found := strings.CutPrefix(line, prefix); found {
			return rest, true
		}
	}
	return "", false
}
