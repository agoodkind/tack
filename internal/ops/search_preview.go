package ops

import (
	"context"
	"encoding/json"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// recordedSearchPreview returns the dry run of a search command. The dry run
// records an operator read through clispec.RunRecordedPreview with the read
// definition that clispec.PreviewSpec derives from spec, and the read event
// stores the digest of the preview output. It prints the preview output after
// the read lifecycle store saves the read event.
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
		return clispec.RunRecordedPreview(ctx, f, clispec.PreviewSpec(spec), encoded,
			func(ctx context.Context, sink clispec.ResultSink) ([]audit.Target, error) {
				return nil, preview(ctx, sink)
			})
	}
}
