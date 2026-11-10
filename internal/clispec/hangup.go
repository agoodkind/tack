package clispec

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"goodkind.io/tack/internal/audit"
)

// datagenVerbs returns the datagen audit verbs that permit ignoring SIGHUP.
func datagenVerbs() []audit.Verb {
	return []audit.Verb{
		audit.VerbOpsDatagenSeed, audit.VerbOpsDatagenSoak, audit.VerbOpsDatagenSearch,
		audit.VerbOpsDatagenRepair, audit.VerbOpsDatagenLegacyLedgerRow,
	}
}

// ignoresHangup reports whether a command is a dry run, is read-only, or
// records a datagen audit verb.
func ignoresHangup(spec audit.Spec, execute bool) bool {
	if !execute || !spec.Mutates {
		return true
	}
	return slices.Contains(datagenVerbs(), audit.Verb(spec.Verb))
}

// applyHangupPolicy uses [signal.Notify] to log SIGHUP without ending a
// command when ignoresHangup permits it. The caller must call the returned
// function after the command returns to stop notification with [signal.Stop]
// and end the goroutine. Other commands retain the default SIGHUP action.
func applyHangupPolicy(ctx context.Context, spec audit.Spec, execute bool) func() {
	if !ignoresHangup(spec, execute) {
		return func() {}
	}
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "cli.hangup_watch_panicked", slog.String("err", fmt.Sprint(recovered)))
			}
		}()
		for {
			select {
			case <-stop:
				return
			case received := <-hangups:
				logHangupIgnored(ctx, received, spec.Verb)
			}
		}
	}()
	return func() {
		signal.Stop(hangups)
		close(stop)
		<-stopped
	}
}

// logHangupIgnored logs one cli.hangup_ignored Info record with the received
// signal and the command's audit verb.
func logHangupIgnored(ctx context.Context, received os.Signal, verb string) {
	slog.InfoContext(ctx, "cli.hangup_ignored", slog.String("signal", received.String()), slog.String("verb", verb))
}
