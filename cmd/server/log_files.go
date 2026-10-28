package main

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/config"
)

const serveCommandName = "serve"

type processLogs struct {
	JSONFile   string
	TextFile   string
	CommandDir string
}

// A one-shot command must not write a log file that another process rotates.
func processLogFiles(cfg *config.Config, root *cobra.Command, args []string, requestID string) processLogs {
	target, _, err := root.Find(args)
	server := err == nil && (target == root || target.Name() == serveCommandName)
	if server || cfg.LogCommandDir == "" {
		return processLogs{JSONFile: cfg.LogJSONFile, TextFile: cfg.LogTextFile, CommandDir: ""}
	}
	name := root.Name()
	if err == nil {
		name = strings.ReplaceAll(strings.TrimPrefix(target.CommandPath(), root.Name()+" "), " ", "-")
	}
	base := filepath.Join(cfg.LogCommandDir, name+"-"+requestID)
	return processLogs{
		JSONFile:   commandLogFile(cfg.LogJSONFile, base+".jsonl"),
		TextFile:   commandLogFile(cfg.LogTextFile, base+".log"),
		CommandDir: cfg.LogCommandDir,
	}
}

func commandLogFile(shared, command string) string {
	if shared == "" {
		return ""
	}
	return command
}
