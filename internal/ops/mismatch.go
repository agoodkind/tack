package ops

import (
	"fmt"
	"strconv"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
)

type searchMappingVersionError struct {
	actual   string
	expected string
}

func (err *searchMappingVersionError) Error() string {
	return "search mapping version " + strconv.Quote(
		err.actual,
	) + " does not match " + strconv.Quote(err.expected)
}

type searchTokenizerDigestError struct {
	actual   string
	expected string
}

func (err *searchTokenizerDigestError) Error() string {
	return "search mapping tokenizer SHA-256 " + strconv.Quote(
		err.actual,
	) + " does not match " + strconv.Quote(err.expected)
}

type searchTopologyMismatchError struct {
	actual   search.PhysicalSettings
	expected config.SearchTopology
}

func (err *searchTopologyMismatchError) Error() string {
	return fmt.Sprintf(
		"search topology %d/%d/%d does not match %d/%d/%d",
		err.actual.Primaries,
		err.actual.RoutingShards,
		err.actual.Replicas,
		err.expected.Primaries,
		err.expected.RoutingShards,
		err.expected.Replicas,
	)
}
