package datagen

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
)

// SeedRunOptions controls one seed generator run.
type SeedRunOptions struct {
	Scale          string
	Seed           int64
	Commit         bool
	RedactAuditPII bool
}

type runSession struct {
	graph             *runtime.Graph
	driver            *Driver
	content           *Content
	identities        Identities
	scale             Scale
	seed              int64
	dryRun            bool
	productionAuth    bool
	synchronousAudit  bool
	seedCorpusStarted func()
}

func openRunSession(
	ctx context.Context,
	cfg *config.Config,
	scaleName string,
	seed int64,
	commit bool,
) (*runSession, error) {
	scale, err := ParseScale(scaleName)
	if err != nil {
		return nil, err
	}
	content, err := NewContent(ctx, seed)
	if err != nil {
		return nil, err
	}
	session := &runSession{
		content: content, scale: scale, seed: seed, dryRun: !commit,
		productionAuth: cfg.Env != "development",
		synchronousAudit: len(audit.SplitBrokers(cfg.AuditKafkaBrokers)) == 0 &&
			cfg.AuditWriterDSN != "",
	}
	if !commit {
		session.identities = PlanIdentities(cfg, seed, scale)
		session.driver = NewDriver(nil, true, seed)
		return session, nil
	}
	session.graph, err = runtime.BuildGraph(ctx, cfg)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.runtime_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf(
			"qa datagen: build runtime; run where FDB is reachable and the app audit environment is present: %w",
			err,
		)
	}
	session.identities, err = BootstrapIdentities(ctx, cfg, seed, scale)
	if err != nil {
		session.close()
		return nil, err
	}
	session.driver = NewDriver(session.graph, false, seed)
	return session, nil
}

func (s *runSession) close() {
	if s.graph != nil {
		s.graph.Close()
	}
}

func (s *runSession) seedCorpus(
	ctx context.Context,
	redactAuditPII bool,
) (Summary, error) {
	if s.seedCorpusStarted != nil {
		s.seedCorpusStarted()
	}
	generator := NewGenerator(
		s.driver,
		s.content,
		s.identities,
		s.scale,
		s.seed,
		GeneratorOptions{
			DryRun:           s.dryRun,
			ProductionAuth:   s.productionAuth,
			RedactAuditPII:   redactAuditPII,
			SynchronousAudit: s.synchronousAudit,
		},
	)
	return generator.Run(ctx)
}

// RunSeed executes one seed generator session after the caller applies guardrails.
func RunSeed(
	ctx context.Context,
	cfg *config.Config,
	options SeedRunOptions,
) (Summary, error) {
	session, err := openRunSession(
		ctx,
		cfg,
		options.Scale,
		options.Seed,
		options.Commit,
	)
	if err != nil {
		return Summary{}, err
	}
	defer session.close()
	return session.seedCorpus(ctx, options.RedactAuditPII)
}
