// docker_progress.go drains the json-line progress stream the Docker daemon
// writes for pulls and loads, so a failure inside the stream fails the caller
// instead of scrolling past.

package ops

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

// drainProgressStream reads docker's json-line progress stream, surfacing
// any error line as a hard fail. Marshal/Unmarshal shape opts out of the
// boundary-event rule; the caller's own info log brackets each invocation.
func drainProgressStream(
	ctx context.Context,
	log *slog.Logger,
	body io.Reader,
	eventName string,
) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var msg map[string]json.RawMessage
		decErr := json.Unmarshal(line, &msg)
		if decErr != nil {
			continue
		}
		if errRaw, ok := msg["error"]; ok {
			var errStr string
			_ = json.Unmarshal(errRaw, &errStr)
			log.ErrorContext(ctx, eventName+".daemon_error",
				slog.String("err", errStr))
			return fmt.Errorf("%s: %s", eventName, errStr)
		}
		if log != nil {
			log.DebugContext(ctx, eventName, slog.String("line", string(line)))
		}
	}
	if err := scanner.Err(); err != nil {
		log.ErrorContext(ctx, eventName+".scan_failed",
			slog.String("err", err.Error()))
		return fmt.Errorf("%s scan: %w", eventName, err)
	}
	return nil
}
