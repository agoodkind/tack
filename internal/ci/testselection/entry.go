package testselection

import (
	"fmt"
	"go/build"
	"log/slog"
	"os"
)

// Main reads command arguments and prints selections.
//
// The executable delegates its entry point to Main.
//
//	err := testselection.Main()
func Main() error {
	root, err := os.Getwd()
	if err != nil {
		slog.Error("testselection.directory_failed", slog.Any("error", err))

		return fmt.Errorf("read working directory: %w", err)
	}

	return Run(root, os.Args[1:], os.Stdout, build.Default)
}
