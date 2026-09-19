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

// cliStep is the tool's stand-in for a test: it satisfies testenv.T, writes
// failure reasons to stderr, and ends the step the way [testing.T.FailNow]
// ends a test, by exiting the step's goroutine.
type cliStep struct {
	failed atomic.Bool
}

// Helper is a no-op outside a test binary.
func (step *cliStep) Helper() {}

// Context is the step's context. The tool runs one step per process and an
// interrupt ends the process, so nothing cancels it sooner.
func (step *cliStep) Context() context.Context { return context.Background() }

// Output is where a failing helper writes its reason.
func (step *cliStep) Output() io.Writer { return os.Stderr }

// FailNow marks the step failed and ends its goroutine.
func (step *cliStep) FailNow() {
	step.failed.Store(true)
	runtime.Goexit()
}

// SkipNow ends the step without failing it. The tool never runs under
// -short, so no helper asks for it.
func (step *cliStep) SkipNow() { runtime.Goexit() }

// fail writes err and fails the step.
func (step *cliStep) fail(err error) {
	_, _ = fmt.Fprintf(step.Output(), "testenv: %v\n", err)
	step.FailNow()
}

// runStep runs body on its own goroutine, so FailNow can end it without
// ending the process, and returns the exit code. A failed step releases the
// engines it had started, since nobody will use them.
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
