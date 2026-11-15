package ops

import (
	"context"
	"encoding/json"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// recordedSearchPreview records access before running the command preview.
func recordedSearchPreview(
	f *cli.Factory,
	spec audit.Spec,
	input func() (json.RawMessage, error),
	preview func(context.Context, clispec.ResultSink) error,
) func(context.Context) error {
	return func(ctx context.Context) error {
		encoded, err := input()
		if err != nil {
			return err
		}

		return clispec.RunRecordedPreview(ctx, f, clispec.PreviewSpec(spec), encoded, preview)
	}
}
