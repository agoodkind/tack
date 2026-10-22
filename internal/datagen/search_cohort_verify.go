package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
)

const (
	cohortWaitDeadline = 15 * time.Minute
	cohortPollInterval = 10 * time.Second
	matchRankedTargets = "ranked_targets"
	matchExact         = "exact"
)

// SearchCohortVerification is the outcome of one cohort verification.
// Verified is true when Cases is nonempty and every case passes.
type SearchCohortVerification struct {
	Verified      bool               `json:"verified"`
	WaitCompleted bool               `json:"wait_completed"`
	WaitDuration  string             `json:"wait_duration"`
	Cases         []SearchCohortCase `json:"cases"`
}

// SearchCohortCase is the outcome of one manifest case.
// Ranks records each expected node's search position starting at 1.
// A value of 0 means the traversal did not return the node.
type SearchCohortCase struct {
	Query      string `json:"query"`
	NodeType   string `json:"node_type"`
	Match      string `json:"match"`
	RankLimit  int    `json:"rank_limit"`
	Passed     bool   `json:"passed"`
	Ranks      []int  `json:"ranks"`
	Returned   int    `json:"returned"`
	Unexpected int    `json:"unexpected"`
	Error      string `json:"error,omitempty"`
}

// cohortSession searches as one workspace actor through authenticated MCP.
type cohortSession struct {
	driver *Driver
	token  string
	entry  string
	limits searchLimits
}

// VerifySearchCohort prepares the cohort that PrepareSearchManifest writes
// for seed from the embedded corpus and checks every manifest case.
// VerifySearchCohort requires separate search workers to index the cohort.
func VerifySearchCohort(ctx context.Context, cfg *config.Config, seed int64) (SearchManifest, SearchCohortVerification, error) {
	var verification SearchCohortVerification
	if err := ValidateTarget(cfg); err != nil {
		return SearchManifest{}, verification, loggedError(ctx, "qa datagen: validate cohort target", err)
	}
	if !cfg.SearchPublicEnabled {
		return SearchManifest{}, verification, loggedError(ctx, "qa datagen: verify cohort", errPublicSearchDisabled)
	}
	limits, err := config.LoadSearchQuerySettings(ctx)
	if err != nil {
		return SearchManifest{}, verification, loggedError(ctx, "qa datagen: load search settings for cohort", err)
	}
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return SearchManifest{}, verification, err
	}
	graph, err := runtime.BuildGraph(ctx, cfg)
	if err != nil {
		return SearchManifest{}, verification, loggedError(ctx, "qa datagen: build runtime for cohort verification", err)
	}
	defer graph.Close()
	identities, err := BootstrapIdentities(ctx, cfg, seed, scale)
	if err != nil {
		return SearchManifest{}, verification, err
	}
	manifest, err := PrepareSearchManifest(ctx, cfg, seed, nil)
	if err != nil {
		return manifest, verification, err
	}
	workspace := identities.Workspaces[0]
	if workspace.Slug != manifest.WorkspaceReference {
		return manifest, verification, fmt.Errorf("qa datagen: cohort workspace %s differs from actor workspace %s", manifest.WorkspaceReference, workspace.Slug)
	}
	session := cohortSession{
		driver: NewDriver(graph, false, seed), token: workspace.Actors[0].Token, entry: workspace.Slug,
		limits: searchLimits{MaxResults: limits.MaxResults, MaxResponseBytes: limits.MaxResponseBytes},
	}
	verification, err = session.verify(ctx, manifest.Cases)
	return manifest, verification, err
}

func (s cohortSession) verify(ctx context.Context, cases []SearchManifestCase) (SearchCohortVerification, error) {
	waited, completed, err := s.waitForTargets(ctx, cases)
	if err != nil {
		return SearchCohortVerification{}, err
	}
	results := make([]SearchCohortCase, 0, len(cases))
	verified := len(cases) > 0
	for _, testCase := range cases {
		result := s.checkCase(ctx, testCase)
		slog.DebugContext(ctx, "qa.datagen.cohort_case_checked", slog.String("query", result.Query),
			slog.String("node_type", result.NodeType), slog.String("match", result.Match),
			slog.Bool("passed", result.Passed), slog.String("ranks", fmt.Sprint(result.Ranks)), slog.String("error", result.Error))
		verified = verified && result.Passed
		results = append(results, result)
	}
	slog.InfoContext(ctx, "qa.datagen.cohort_checked", slog.Bool("verified", verified),
		slog.Bool("wait_completed", completed), slog.Duration("wait_duration", waited), slog.Int("cases", len(results)))
	return SearchCohortVerification{Verified: verified, WaitCompleted: completed, WaitDuration: waited.String(), Cases: results}, nil
}

// waitForTargets searches every case until each traversal returns every
// expected node or cohortWaitDeadline passes.
// missingTargets counts every expected node in a case as missing
// when the case search fails.
func (s cohortSession) waitForTargets(ctx context.Context, cases []SearchManifestCase) (time.Duration, bool, error) {
	started := clock.Now()
	deadline := started.Add(cohortWaitDeadline)
	for {
		missing := s.missingTargets(ctx, cases)
		elapsed := clock.Now().Sub(started)
		if missing == 0 {
			return elapsed, true, nil
		}
		slog.DebugContext(ctx, "qa.datagen.cohort_waiting", slog.Int("missing_targets", missing), slog.Duration("elapsed", elapsed))
		if clock.Now().After(deadline) {
			return elapsed, false, nil
		}
		timer := time.NewTimer(cohortPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return elapsed, false, loggedError(ctx, "qa datagen: wait for cohort targets", ctx.Err())
		case <-timer.C:
		}
	}
}
