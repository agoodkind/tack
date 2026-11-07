//go:build pgoverlay

package pgoverlay_test

import (
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	pacerRunSlice       = 2 * time.Millisecond
	pacerStopSlice      = 10 * time.Millisecond
	stoppedStateTimeout = 5 * time.Second
)

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

// freeze waits for the pacing loop to end. Call requireProvidersStopped
// to verify that the provider processes have stopped.
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
