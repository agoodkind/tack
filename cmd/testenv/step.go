package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"

	"goodkind.io/tack/internal/testenv"
)

// cliStep implements testenv.T for this tool. It writes failure reasons to
// stderr and ends the step's goroutine, as [testing.T.FailNow] ends a test's
// goroutine.
type cliStep struct {
	failed atomic.Bool
}

// Helper does not perform an operation outside a test binary.
func (step *cliStep) Helper() {}

// Context returns the step's context. The tool runs one step per process.
// An interrupt ends the process. The tool does not cancel this context.
func (step *cliStep) Context() context.Context { return context.Background() }

// Output returns the destination for a failing helper's reason.
func (step *cliStep) Output() io.Writer { return os.Stderr }

// FailNow marks the step failed and ends its goroutine.
func (step *cliStep) FailNow() {
	step.failed.Store(true)
	runtime.Goexit()
}

// SkipNow ends the step without failing it. The tool does not run with -short,
// and helpers do not request skipping for that flag.
func (step *cliStep) SkipNow() { runtime.Goexit() }

// fail writes err and fails the step.
func (step *cliStep) fail(err error) {
	_, _ = fmt.Fprintf(step.Output(), "testenv: %v\n", err)
	step.FailNow()
}

// runStep runs body in its own goroutine and returns the exit code. FailNow
// ends that goroutine without ending the process. If the step fails, runStep
// releases the engines it started.
func runStep(body func(*cliStep)) int {
	step := &cliStep{failed: atomic.Bool{}}
	var _ testenv.T = step
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("testenv.step_panicked", slog.String("err", fmt.Sprint(recovered)))
				step.failed.Store(true)
			}
		}()
		body(step)
	}()
	<-done
	if !step.failed.Load() {
		return 0
	}
	if err := testenv.Release(step.Context()); err != nil {
		slog.Error("testenv.release_failed", slog.String("err", err.Error()))
	}
	return 1
}
