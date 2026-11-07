//go:build pgoverlay

package pgoverlay_test

import (
	"os"
	"strconv"
	"strings"
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
