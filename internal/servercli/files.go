package servercli

import (
	"path/filepath"
	"strings"

	"goodkind.io/tack/internal/config"
)

const serveCommandName = "serve"

type processLogs struct {
	JSONFile   string
	TextFile   string
	CommandDir string
}

// A one-shot command must not write a log file that another process rotates.
func processLogFiles(cfg *config.Config, server bool, commandName, requestID string) processLogs {
	if server || cfg.LogCommandDir == "" {
		return processLogs{JSONFile: cfg.LogJSONFile, TextFile: cfg.LogTextFile, CommandDir: ""}
	}

	name := strings.ReplaceAll(commandName, " ", "-")
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
