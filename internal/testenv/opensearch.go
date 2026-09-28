package testenv

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
)

const openSearchImage = "opensearchproject/opensearch:3.8.0"

// SearchIntegrationVariable must equal "1" for a test binary to start an
// OpenSearch engine. The default engine uses 8 GiB of memory. A
// memory-failure test sets a smaller limit. Every engine downloads the 555 MB
// model. The general test jobs leave it unset and skip every OpenSearch-backed
// test.
// The search integration job and the validation runner set it.
const SearchIntegrationVariable = "TACK_SEARCH_INTEGRATION"

// openSearchMemoryBytes is the container memory limit of the default engine,
// the per-guest floor the model requires.
const openSearchMemoryBytes int64 = 8 << 30

// OpenSearchFixture supplies the real TLS engine to search integration tests.
type OpenSearchFixture struct {
	Endpoint string
	CA       string
	Username string
	Password string
	// Container is the engine container, which a test stops and starts to
	// reproduce an outage. The endpoint address stays assigned while it is
	// stopped.
	Container string
}

// openSearchEngine stores the outcome of starting one engine for one memory
// limit, shared by every caller in the process.
type openSearchEngine struct {
	once    sync.Once
	fixture OpenSearchFixture
	err     error
}

var openSearchEngines struct {
	sync.Mutex
	byMemory map[int64]*openSearchEngine
}

// OpenSearch starts one isolated OpenSearch engine with the default 8 GiB
// memory limit for this test process.
func OpenSearch(t T) OpenSearchFixture {
	t.Helper()
	return OpenSearchWithMemory(t, openSearchMemoryBytes)
}

// OpenSearchWithMemory starts one isolated OpenSearch engine per container
// memory limit for this test process. A limit below the model's floor lets a
// test reproduce the ML memory circuit breaker.
func OpenSearchWithMemory(t T, memoryBytes int64) OpenSearchFixture {
	t.Helper()
	skipWhenShort(t)
	skipWithoutSearchIntegration(t)
	engine := openSearchEngineFor(memoryBytes)
	engine.once.Do(func() {
		ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
		defer cancel()
		engine.fixture, engine.err = provisionOpenSearch(ctx, memoryBytes)
	})
	if engine.err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", engine.err)
		t.FailNow()
	}
	return engine.fixture
}

// skipWithoutSearchIntegration skips an OpenSearch-backed test unless
// [SearchIntegrationVariable] is "1". Outside a test binary, cmd/testenv
// starts the engine on request.
func skipWithoutSearchIntegration(t T) {
	t.Helper()
	if testing.Testing() && os.Getenv(SearchIntegrationVariable) != "1" {
		_, _ = fmt.Fprintf(t.Output(), "testenv: OpenSearch-backed test skipped; set %s=1 to run it\n", SearchIntegrationVariable)
		t.SkipNow()
	}
}

func openSearchEngineFor(memoryBytes int64) *openSearchEngine {
	openSearchEngines.Lock()
	defer openSearchEngines.Unlock()
	if openSearchEngines.byMemory == nil {
		openSearchEngines.byMemory = map[int64]*openSearchEngine{}
	}
	engine, exists := openSearchEngines.byMemory[memoryBytes]
	if !exists {
		engine = &openSearchEngine{
			once:    sync.Once{},
			fixture: OpenSearchFixture{Endpoint: "", CA: "", Username: "", Password: "", Container: ""},
			err:     nil,
		}
		openSearchEngines.byMemory[memoryBytes] = engine
	}
	return engine
}
