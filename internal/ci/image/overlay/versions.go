package overlay

import (
	"errors"
	"fmt"
	"os"
	"regexp"
)

const (
	makefilePath = "Makefile"
	composePath  = "docker-compose.yml"
)

var (
	fdbVersionPattern = regexp.MustCompile(`(?m)^FDB_VERSION[ \t]*\?=[ \t]*(\S+)`)
	errNoFDBVersion   = errors.New("could not find FDB_VERSION")
)

func fdbVersion() (string, error) {
	content, err := os.ReadFile(makefilePath)
	if err != nil {
		return "", &os.PathError{Op: "read Makefile", Path: makefilePath, Err: err}
	}

	match := fdbVersionPattern.FindSubmatch(content)
	if match == nil {
		return "", fmt.Errorf("%w in %s", errNoFDBVersion, makefilePath)
	}

	return string(match[1]), nil
}
