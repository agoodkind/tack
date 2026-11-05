//go:build pgoverlay

package pgoverlay_test

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	procDirectory      = "/proc"
	processFindTimeout = 30 * time.Second
	processFindPeriod  = 50 * time.Millisecond
	// statParentIndex is the position of the parent PID among the fields of
	// /proc/<pid>/stat that follow the command name in parentheses.
	statParentIndex = 1

	pacerRunSlice  = 2 * time.Millisecond
	pacerStopSlice = 10 * time.Millisecond

	stoppedStateTimeout = 5 * time.Second
)

// processInfo is one running process of the runner container.
type processInfo struct {
	pid       int
	parentPID int
	arguments []string
}

// listProcesses reads every process that /proc shows. The test process runs in
// the same container as the tofu commands, so /proc contains them.
func listProcesses(t *testing.T) []processInfo {
	t.Helper()
	processes, err := readProcesses()
	if err != nil {
		t.Fatalf("read %s: %v", procDirectory, err)
	}
	return processes
}

func readProcesses() ([]processInfo, error) {
	entries, err := os.ReadDir(procDirectory)
	if err != nil {
		return nil, err
	}
	var processes []processInfo
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		info, ok := readProcess(pid)
		if ok {
			processes = append(processes, info)
		}
	}
	return processes, nil
}

// readProcess returns false when the process ended while it was read.
func readProcess(pid int) (processInfo, bool) {
	command, err := os.ReadFile(procDirectory + "/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return processInfo{}, false
	}
	stat, err := os.ReadFile(procDirectory + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return processInfo{}, false
	}
	text := string(stat)
	afterName := strings.Fields(text[strings.LastIndex(text, ")")+1:])
	if len(afterName) <= statParentIndex {
		return processInfo{}, false
	}
	parent, err := strconv.Atoi(afterName[statParentIndex])
	if err != nil {
		return processInfo{}, false
	}
	arguments := strings.Split(strings.TrimRight(string(command), "\x00"), "\x00")
	return processInfo{pid: pid, parentPID: parent, arguments: arguments}, true
}

// findTofuApply returns the PID of the tofu apply process for a working directory.
func findTofuApply(t *testing.T, directory string) int {
	t.Helper()
	deadline := time.Now().Add(processFindTimeout)
	for time.Now().Before(deadline) {
		for _, info := range listProcesses(t) {
			if len(info.arguments) > 2 && strings.HasSuffix(info.arguments[0], "tofu") &&
				info.arguments[1] == "-chdir="+directory && info.arguments[2] == "apply" {
				return info.pid
			}
		}
		time.Sleep(processFindPeriod)
	}
	t.Fatalf("no tofu apply process for %s appeared within %s", directory, processFindTimeout)
	return 0
}

// waitForProvider waits until tofu has started a provider process.
func waitForProvider(t *testing.T, tofuPID int) {
	t.Helper()
	deadline := time.Now().Add(processFindTimeout)
	for time.Now().Before(deadline) {
		providers, err := providerPIDs(tofuPID)
		if err != nil {
			t.Fatalf("read provider processes: %v", err)
		}
		if len(providers) > 0 {
			return
		}
		time.Sleep(processFindPeriod)
	}
	t.Fatalf("tofu process %d started no provider within %s", tofuPID, processFindTimeout)
}

// providerPIDs returns the provider processes that are children of tofu. Tofu
// can start more than one provider process during an apply. The pacer reads the
// list on every cycle.
func providerPIDs(tofuPID int) ([]int, error) {
	processes, err := readProcesses()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, info := range processes {
		if info.parentPID == tofuPID && strings.HasSuffix(info.arguments[0], "terraform-provider-postgresql") {
			pids = append(pids, info.pid)
		}
	}
	return pids, nil
}

func signalAll(pids []int, signal syscall.Signal) {
	for _, pid := range pids {
		_ = syscall.Kill(pid, signal)
	}
}

// providerPacer runs the provider processes of one tofu process in short
// bursts: it resumes them, waits, stops them, and waits again. They still make
// progress, and freeze leaves them stopped.
type providerPacer struct {
	once     sync.Once
	tofuPID  int
	quit     chan struct{}
	finished chan struct{}
	err      error
}

func startPacer(tofuPID int) *providerPacer {
	pacer := &providerPacer{tofuPID: tofuPID, quit: make(chan struct{}), finished: make(chan struct{})}
	go pacer.run()
	return pacer
}

func (pacer *providerPacer) run() {
	defer close(pacer.finished)
	for {
		select {
		case <-pacer.quit:
			providers, err := providerPIDs(pacer.tofuPID)
			if err != nil {
				pacer.err = err
				return
			}
			signalAll(providers, syscall.SIGSTOP)
			return
		default:
		}
		providers, err := providerPIDs(pacer.tofuPID)
		if err != nil {
			pacer.err = err
			return
		}
		signalAll(providers, syscall.SIGCONT)
		time.Sleep(pacerRunSlice)
		signalAll(providers, syscall.SIGSTOP)
		time.Sleep(pacerStopSlice)
	}
}

// freeze stops the cycle and returns after the provider processes are stopped.
func (pacer *providerPacer) freeze() error {
	pacer.once.Do(func() { close(pacer.quit) })
	<-pacer.finished
	return pacer.err
}

// requireProvidersStopped requires at least one provider process of tofu, all
// in the stopped state. The kernel applies a stop signal asynchronously. The
// function polls until every provider reports the stopped state.
func requireProvidersStopped(t *testing.T, tofuPID int) {
	t.Helper()
	deadline := time.Now().Add(stoppedStateTimeout)
	for {
		providers, err := providerPIDs(tofuPID)
		if err != nil {
			t.Fatalf("read provider processes: %v", err)
		}
		if len(providers) == 0 {
			t.Fatalf("tofu process %d has no provider process after the rename", tofuPID)
		}
		running := 0
		for _, pid := range providers {
			if processState(t, pid) != "T" {
				running++
			}
		}
		if running == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d provider processes of tofu %d are not stopped within %s", running, tofuPID, stoppedStateTimeout)
		}
		time.Sleep(processFindPeriod)
	}
}

// processState returns the state letter that /proc reports, which is T for a
// stopped process.
func processState(t *testing.T, pid int) string {
	t.Helper()
	stat, err := os.ReadFile(procDirectory + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatalf("read the state of process %d: %v", pid, err)
	}
	text := string(stat)
	return strings.Fields(text[strings.LastIndex(text, ")")+1:])[0]
}

// processTree returns root and every process below it.
func processTree(t *testing.T, root int) []int {
	t.Helper()
	processes := listProcesses(t)
	tree := []int{root}
	for index := 0; index < len(tree); index++ {
		for _, info := range processes {
			if info.parentPID == tree[index] {
				tree = append(tree, info.pid)
			}
		}
	}
	return tree
}

// killTree sends SIGKILL to every process in the tree and returns how many
// processes it signaled.
func killTree(t *testing.T, tree []int) int {
	t.Helper()
	signaled := 0
	for _, pid := range tree {
		err := syscall.Kill(pid, syscall.SIGKILL)
		if err == nil {
			signaled++
			continue
		}
		if err != syscall.ESRCH {
			t.Fatalf("kill process %d: %v", pid, err)
		}
	}
	return signaled
}
