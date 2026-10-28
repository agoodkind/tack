package main

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/config"
)

// serveCommandName is the subcommand that starts the server. The bare root command also
// starts the server.
const serveCommandName = "serve"

// processLogFiles returns the JSON and text log paths for the command that args select. The
// server writes cfg.LogJSONFile and cfg.LogTextFile. When cfg.LogCommandDir is set, every
// other command writes its own files in that directory. Lumberjack in each process rotates a
// shared file by renaming and compressing it, and another process that still has the renamed
// file open then writes into a deleted file. An empty path stays empty, because empty
// disables that handler.
func processLogFiles(cfg *config.Config, root *cobra.Command, args []string, requestID string) (string, string) {
	target, _, err := root.Find(args)
	server := err == nil && (target == root || target.Name() == serveCommandName)
	if server || cfg.LogCommandDir == "" {
		return cfg.LogJSONFile, cfg.LogTextFile
	}
	name := root.Name()
	if err == nil {
		name = strings.ReplaceAll(strings.TrimPrefix(target.CommandPath(), root.Name()+" "), " ", "-")
	}
	base := filepath.Join(cfg.LogCommandDir, name+"-"+requestID)
	return commandLogFile(cfg.LogJSONFile, base+".jsonl"), commandLogFile(cfg.LogTextFile, base+".log")
}

func commandLogFile(shared, command string) string {
	if shared == "" {
		return ""
	}
	return command
}
