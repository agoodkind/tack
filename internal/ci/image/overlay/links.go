package overlay

import (
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
)

// maxLinkHops bounds link resolution so a link cycle fails instead of
// looping.
const maxLinkHops = 40

const (
	rootPath         = "/"
	emptyComponent   = ""
	currentComponent = "."
	parentComponent  = ".."
)

var (
	errLinkEscapesRoot = errors.New("link escapes the image root")
	errLinkCycle       = errors.New("link resolution exceeded the hop limit")
)

// link is a symbolic or hard link entry from the merged image filesystem.
type link struct {
	target string
	hard   bool
}

// normalize returns the absolute image path for an archive entry name.
func normalize(entryName string) string {
	return path.Clean("/" + entryName)
}

func splitPath(value string) []string {
	return strings.Split(value, "/")
}

// resolve follows links in every component of target and returns the final
// absolute image path. It rejects a link that leaves the image root and a
// chain longer than maxLinkHops.
func resolve(links map[string]link, target string) (string, error) {
	pending := splitPath(normalize(target))
	current := rootPath
	hops := 0

	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]

		switch {
		case part == emptyComponent || part == currentComponent:
		case part == parentComponent:
			if current == rootPath {
				return "", resolveError(target, hops, errLinkEscapesRoot)
			}

			current = path.Dir(current)
		default:
			next := path.Join(current, part)

			found, isLink := links[next]
			if !isLink {
				current = next

				break
			}

			hops++

			if hops > maxLinkHops {
				return "", resolveError(target, hops, errLinkCycle)
			}

			if found.hard || strings.HasPrefix(found.target, rootPath) {
				current = rootPath
			}

			pending = append(splitPath(found.target), pending...)
		}
	}

	return current, nil
}

// resolveError logs and wraps a link resolution failure for target.
func resolveError(target string, hops int, cause error) error {
	slog.Error(
		"overlay.link_resolution_failed",
		slog.String("path", target),
		slog.Int("hops", hops),
		slog.Any("error", cause),
	)

	return fmt.Errorf("resolve %s: %w", target, cause)
}
