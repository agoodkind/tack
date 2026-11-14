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

// cliStep implements testenv.T for the tool. It writes failures to stderr
// and ends the step's goroutine as testing.T.FailNow ends a test.
type cliStep struct {
	failed atomic.Bool
}

// Helper does not perform an action outside a test binary.
func (step *cliStep) Helper() {}

// Context returns a background context. The tool runs one step per process.
// An interrupt ends the process rather than canceling its context.
func (step *cliStep) Context() context.Context { return context.Background() }

// Output returns stderr for helper failure messages.
func (step *cliStep) Output() io.Writer { return os.Stderr }

// FailNow marks the step failed and ends its goroutine.
func (step *cliStep) FailNow() {
	step.failed.Store(true)
	runtime.Goexit()
}

// SkipNow ends the step without failing it. Helpers do not request skipping
// because the tool does not run with -short.
func (step *cliStep) SkipNow() { runtime.Goexit() }

// fail writes err and fails the step.
func (step *cliStep) fail(err error) {
	_, _ = fmt.Fprintf(step.Output(), "testenv: %v\n", err)
	step.FailNow()
}

// runStep runs body in a separate goroutine and returns the exit code.
// FailNow can end that goroutine without ending the process. Failed steps
// release the engines they started.
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
