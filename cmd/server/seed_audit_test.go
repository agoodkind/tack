package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
)

type failingSeedOutbox struct {
	calls int
}

func (o *failingSeedOutbox) WriteOutbox(context.Context, audit.Event) error {
	o.calls++
	return errors.New("outbox unavailable")
}

func TestSeedRefusesWhenIntentCannotRecord(t *testing.T) {
	outbox := &failingSeedOutbox{}
	factory := &cli.Factory{
		Cfg: &config.Config{}, In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
	}
	factory.SetAuditOutbox(outbox)
	root := buildRoot(factory)
	root.SetContext(context.Background())
	root.SetArgs([]string{
		"--execute",
		"--operator-id", "019dd226-440e-729a-a442-281aaf73ca30",
		"--operator-email", "operator@example.com",
		"seed",
	})

	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "record command intent") {
		t.Fatalf("seed error = %v, want intent record failure", err)
	}
	if outbox.calls != 1 {
		t.Fatalf("outbox writes = %d, want 1 before seed work", outbox.calls)
	}
}
