package main

import (
	"goodkind.io/tack/internal/config"
	internalrepair "goodkind.io/tack/internal/repair"
)

func runRepair(cfg *config.Config, args []string) {
	internalrepair.Run(cfg, args)
}

func printRepairUsage() {
	internalrepair.PrintUsage()
}

func printRepairClasses() {
	internalrepair.PrintClasses()
}

func printRepairCommandUsage(command string) {
	internalrepair.PrintCommandUsage(command)
}

func runRepairRead(cfg *config.Config, argv []string) {
	internalrepair.RunRead(cfg, argv)
}

func runRepairFind(cfg *config.Config, argv []string) {
	internalrepair.RunFind(cfg, argv)
}

func runRepairQuery(cfg *config.Config, argv []string) {
	internalrepair.RunQuery(cfg, argv)
}

func runRepairVerify(cfg *config.Config, argv []string) {
	internalrepair.RunVerify(cfg, argv)
}

func runRepairValidate(cfg *config.Config, argv []string) {
	internalrepair.RunValidate(cfg, argv)
}

func runRepairPreview(cfg *config.Config, argv []string) {
	internalrepair.RunPreview(cfg, argv)
}

func runRepairApply(cfg *config.Config, argv []string) {
	internalrepair.RunApply(cfg, argv)
}

func parseRepairSelection(command string, argv []string) internalrepair.Selection {
	return internalrepair.ParseSelection(command, argv)
}
