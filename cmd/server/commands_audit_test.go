package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

func TestEveryRegisteredCommandDeclaresAuditVerb(t *testing.T) {
	factory := &cli.Factory{
		Cfg: &config.Config{},
		In:  strings.NewReader(""),
		Out: &bytes.Buffer{},
		Err: &bytes.Buffer{},
	}
	assertCommandAuditVerb(t, buildRoot(factory))
}

func assertCommandAuditVerb(t *testing.T, command *cobra.Command) {
	t.Helper()
	if command.Name() != "help" && command.Annotations[clispec.GroupAnnotation] != "true" && command.RunE != nil {
		verb := command.Annotations[clispec.AuditVerbAnnotation]
		if verb == "" {
			t.Errorf("command %q has no audit verb", command.CommandPath())
		}
	}
	for _, child := range command.Commands() {
		assertCommandAuditVerb(t, child)
	}
}
