package overlay

import (
	"fmt"
	"os"
	"regexp"
)

const (
	makefilePath = "Makefile"
	composePath  = "docker-compose.yml"
)

var fdbVersionPattern = regexp.MustCompile(`(?m)^FDB_VERSION[ \t]*\?=[ \t]*(\S+)`)

// fdbVersion returns the first FDB_VERSION assignment from the Makefile.
func fdbVersion() (string, error) {
	content, err := os.ReadFile(makefilePath)
	if err != nil {
		return "", &os.PathError{Op: "read Makefile", Path: makefilePath, Err: err}
	}
	match := fdbVersionPattern.FindSubmatch(content)
	if match == nil {
		return "", fmt.Errorf("could not find FDB_VERSION in %s", makefilePath)
	}
	return string(match[1]), nil
}
