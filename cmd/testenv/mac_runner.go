package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"goodkind.io/tack/internal/testenv"
)

func runMac(arguments []string) int {
	options, err := parseMacOptions(arguments)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "testenv run-mac:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := testenv.RunMacTests(ctx, options)
	if ctx.Err() != nil {
		return 130
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "testenv run-mac:", err)
		return 1
	}
	return result.ExitCode
}
