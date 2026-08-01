package datagen

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
)

func TestFallbackSeedStopsBeforeCallsWhenContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	scale, err := ParseScale("small")
	if err != nil {
		t.Fatalf("ParseScale() error = %v", err)
	}
	content, err := NewContent(t.Context(), 245)
	if err != nil {
		t.Fatalf("NewContent() error = %v", err)
	}
	var handlerCallCount atomic.Int64
	var seedStartCount atomic.Int64
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if handlerCallCount.Add(1) == 2 {
			cancel()
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"jsonrpc":"2.0","id":"1","result":{"content":[` +
				`{"type":"text","text":"ok"}]}}`,
		))
	})
	driver := NewDriver(testGraph(handler), false, 245)
	session := &runSession{
		driver:  driver,
		content: content,
		identities: Identities{Workspaces: []WorkspaceIdentity{{
			Slug: "workspace", Actors: []Actor{{Token: "token"}},
		}}},
		scale: scale,
		seed:  245,
		seedCorpusStarted: func() {
			seedStartCount.Add(1)
		},
	}
	_, err = prepareSoakProjects(ctx, context.WithoutCancel(ctx), session)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prepareSoakProjects() error = %v, want context canceled", err)
	}
	if driver.CallCount() != 2 {
		t.Fatalf("prepareSoakProjects() calls = %d, want 2", driver.CallCount())
	}
	if seedStartCount.Load() != 0 {
		t.Fatalf("prepareSoakProjects() seed starts = %d, want 0", seedStartCount.Load())
	}
}

func TestRunSoakDryRunFailsWithoutServerCorpus(t *testing.T) {
	t.Parallel()
	_, err := RunSoak(
		t.Context(),
		t.Context(),
		&config.Config{Env: "development"},
		SoakOptions{
			Duration: time.Minute,
			Rate:     1_000_000,
			MaxOps:   3,
			Scale:    "small",
			Seed:     245,
			Commit:   false,
		},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "no usable projects after fallback seed") {
		t.Fatalf("RunSoak() error = %v", err)
	}
}

func TestValidateSoakOptions(t *testing.T) {
	t.Parallel()
	cases := []SoakOptions{
		{Duration: -time.Second, Rate: 1},
		{Rate: 0},
		{Rate: 1, MaxOps: -1},
	}
	for _, options := range cases {
		if err := validateSoakOptions(options); err == nil {
			t.Fatalf("validateSoakOptions(%#v) error = nil", options)
		}
	}
}

func TestSoakSignalWaitsForInflightCall(t *testing.T) {
	t.Parallel()
	callStarted := make(chan struct{})
	releaseCall := make(chan struct{})
	requestCanceled := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(callStarted)
		select {
		case <-releaseCall:
		case <-request.Context().Done():
			requestCanceled <- struct{}{}
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"jsonrpc":"2.0","id":"1","result":{"content":[` +
				`{"type":"text","text":"ok"}]}}`,
		))
	})
	content, err := NewContent(t.Context(), 245)
	if err != nil {
		t.Fatalf("NewContent() error = %v", err)
	}
	soak := &Soak{
		driver:  NewDriver(testGraph(handler), false, 245),
		content: content,
		projects: []*soakProject{{
			Workspace: WorkspaceIdentity{Actors: []Actor{{Token: "token"}}},
			States:    []soakNode{{RawID: "state", Group: stateGroupCompleted}},
			Issues:    []*soakIssue{{soakNode: soakNode{RawID: "issue"}}},
		}},
		options: SoakOptions{Rate: 1_000_000},
		clock:   clock.Wall{},
	}
	runContext, cancelRun := context.WithCancel(t.Context())
	result := make(chan SoakSummary, 1)
	go func() {
		summary, runErr := soak.Run(
			runContext,
			context.WithoutCancel(runContext),
			runContext.Done(),
		)
		if runErr != nil {
			t.Errorf("Run() error = %v", runErr)
		}
		result <- summary
	}()
	<-callStarted
	cancelRun()
	select {
	case <-requestCanceled:
		close(releaseCall)
		t.Fatal("signal canceled the in-flight request")
	case <-result:
		close(releaseCall)
		t.Fatal("Run() returned before the in-flight call completed")
	case <-time.After(10 * time.Millisecond):
	}
	close(releaseCall)
	select {
	case summary := <-result:
		if summary.StopReason != "signal" || summary.Operations != 1 {
			t.Fatalf("Run() summary = %#v", summary)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after the in-flight call completed")
	}
}
