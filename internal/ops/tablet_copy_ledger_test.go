package ops

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func TestYBPlacementAuditPreservesLedgerContentsAndLiteralPaths(t *testing.T) {
	ctx, cli := scratchDrillDocker(t)
	root := testenv.SharedDir(t)
	const image = "alpine:3"
	if err := ensureImage(ctx, cli, nopLogger(), image); err != nil {
		t.Fatalf("ensure %s: %v", image, err)
	}

	created, err := cli.ContainerCreate(
		ctx,
		client.ContainerCreateOptions{
			Config: &container.Config{Image: image, Cmd: []string{"sleep", "300"}},
			HostConfig: &container.HostConfig{
				Mounts: []mount.Mount{
					{Type: mount.TypeBind, Source: root, Target: root},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("create placement audit container: %v", err)
	}

	t.Cleanup(func() { removeContainerForce(context.WithoutCancel(ctx), cli, created.ID) })
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start placement audit container %s: %v", created.ID, err)
	}

	for _, test := range []struct {
		name           string
		createExpected bool
		createPlaced   bool
		expected       string
		placed         string
		output         string
	}{
		{name: "both ledgers are absent", output: "expected 0\nplaced 0\nmissing\n"},
		{
			name:           "both ledgers are empty",
			createExpected: true,
			createPlaced:   true,
			output:         "expected 0\nplaced 0\nmissing\n",
		},
		{
			name:           "only the expected ledger exists",
			createExpected: true,
			expected:       "table:b\ntable:a\ntable:b\n",
			output:         "expected 2\nplaced 0\nmissing\ntable:a\ntable:b\n",
		},
		{
			name:         "only the placed ledger exists",
			createPlaced: true,
			output:       "expected 0\nplaced 0\nmissing\n",
		},
		{
			name:           "duplicate identities include a missing copy",
			createExpected: true,
			createPlaced:   true,
			expected:       "table:b\ntable:a\ntable:b\ntable:c\n",
			placed:         "table:c\ntable:a\ntable:a\n",
			output:         "expected 3\nplaced 2\nmissing\ntable:b\n",
		},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				directory := filepath.Join(root, test.name, "ledger ' \" $HOME `id` [*]")
				if err := os.MkdirAll(directory, 0o755); err != nil {
					t.Fatalf("create ledger directory %s: %v", directory, err)
				}

				layout := ybPlacementLayout{
					ExportRoot:     "",
					RocksDBDir:     "",
					ExpectedLedger: filepath.Join(directory, "expected ' \" $HOME `id` [*]"),
					PlacedLedger:   filepath.Join(directory, "placed ' \" $HOME `id` [*]"),
				}

				ledgers := []struct {
					path    string
					content string
					create  bool
				}{
					{layout.ExpectedLedger, test.expected, test.createExpected},
					{layout.PlacedLedger, test.placed, test.createPlaced},
				}
				past := time.Unix(1, 0)
				for _, ledger := range ledgers {
					if ledger.create {
						if err := os.WriteFile(
							ledger.path,
							[]byte(ledger.content),
							0o600,
						); err != nil {
							t.Fatalf("write ledger %s: %v", ledger.path, err)
						}

						if err := os.Chtimes(ledger.path, past, past); err != nil {
							t.Fatalf("set ledger timestamp %s: %v", ledger.path, err)
						}
					}
				}

				// A second audit checks the files that the first audit created.
				for range 2 {
					result, err := containerExec(
						ctx,
						cli,
						created.ID,
						[]string{"sh", "-c", ybPlacementAuditScript(layout)},
					)
					if err != nil || result.ExitCode != 0 {
						t.Fatalf(
							"execute placement audit: exit %d, error %v, stderr %q",
							result.ExitCode,
							err,
							result.Stderr,
						)
					}

					if result.Stdout != test.output {
						t.Fatalf("audit output = %q, want %q", result.Stdout, test.output)
					}

					if _, err := parseYBPlacementAudit(result.Stdout); err != nil {
						t.Fatalf("parse placement audit %q: %v", result.Stdout, err)
					}

					for _, ledger := range ledgers {
						content, err := os.ReadFile(ledger.path)
						if err != nil {
							t.Fatalf("read literal ledger path %s: %v", ledger.path, err)
						}

						info, err := os.Stat(ledger.path)
						if err != nil {
							t.Fatalf("stat ledger %s: %v", ledger.path, err)
						}

						if !info.ModTime().After(past) {
							t.Fatalf("ledger %s did not update its modification time", ledger.path)
						}

						if string(content) != ledger.content {
							t.Fatalf(
								"ledger %s contains %q, want %q",
								ledger.path,
								content,
								ledger.content,
							)
						}
					}
				}
			},
		)
	}
}
