package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
)

// Summary reports the completed generator work.
type Summary struct {
	Scale      string
	Seed       int64
	DryRun     bool
	ToolCalls  int64
	Created    int
	Reused     int
	Workspaces int
	Projects   int
	Issues     int
}

// Generator builds the QA corpus through one Driver.
type Generator struct {
	driver           *Driver
	content          *Content
	identities       Identities
	scale            Scale
	seed             int64
	dryRun         bool
	productionAuth bool
	redactAuditPII bool
	redactActor    ActorRedactor
	// probeNodeID is the first generated node's raw id, kept for the
	// cross-org isolation probe.
	probeNodeID string
	summary     Summary
}

// GeneratorOptions controls environment-dependent corpus probes. RedactActor
// is the host redaction path the opt-in PII step runs through; nil means the
// environment carries no reader and redactor DSN and the step is skipped.
type GeneratorOptions struct {
	DryRun         bool
	ProductionAuth bool
	RedactAuditPII bool
	RedactActor    ActorRedactor `exhaustruct:"optional"`
}

// NewGenerator constructs a deterministic corpus generator.
func NewGenerator(
	driver *Driver,
	content *Content,
	identities Identities,
	scale Scale,
	seed int64,
	options GeneratorOptions,
) *Generator {
	generator := &Generator{
		driver: driver, content: content, identities: identities,
		scale: scale, seed: seed, dryRun: options.DryRun,
		productionAuth: options.ProductionAuth,
		redactAuditPII: options.RedactAuditPII,
		redactActor:    options.RedactActor,
		summary: Summary{
			Scale: scale.Name, Seed: seed, DryRun: options.DryRun,
			Workspaces: len(identities.Workspaces),
		},
	}
	registerIdentityTokens(driver, identities)
	return generator
}

func registerIdentityTokens(driver *Driver, identities Identities) {
	driver.registerTokenIdentity(
		identities.ExpiredToken,
		identities.ExpiredRequestToken,
	)
	driver.registerTokenIdentity(identities.BogusToken, identities.BogusRequestToken)
	for _, workspace := range identities.Workspaces {
		for _, actor := range workspace.Actors {
			driver.registerTokenIdentity(actor.Token, actor.RequestToken)
		}
	}
}

// Run generates all requested tenants and exercises negative authentication.
func (g *Generator) Run(ctx context.Context) (Summary, error) {
	if err := g.exerciseRejectedTokens(ctx); err != nil {
		return Summary{}, err
	}
	for workspaceIndex, workspace := range g.identities.Workspaces {
		if err := g.generateWorkspace(ctx, workspaceIndex, workspace); err != nil {
			return Summary{}, err
		}
	}
	if err := g.probeCrossOrgIsolation(ctx); err != nil {
		return Summary{}, err
	}
	if err := g.redactOneActor(ctx); err != nil {
		return Summary{}, err
	}
	g.summary.ToolCalls = g.driver.CallCount()
	return g.summary, nil
}

func (g *Generator) exerciseRejectedTokens(ctx context.Context) error {
	if !g.productionAuth {
		slog.InfoContext(
			ctx,
			"qa.datagen.auth_probes_skipped",
			slog.String("reason", "development auth does not validate SQL tokens"),
		)
		return nil
	}
	probes := []authProbe{
		{label: "expired", token: g.identities.ExpiredToken},
		{label: "bogus", token: g.identities.BogusToken},
	}
	slices.SortFunc(probes, func(left, right authProbe) int {
		return strings.Compare(left.label, right.label)
	})
	for _, probe := range probes {
		_, err := g.driver.Call(
			ctx,
			probe.token,
			"tack_list_workspaces",
			ToolArguments{},
		)
		if g.dryRun {
			continue
		}
		if err == nil {
			return fmt.Errorf(
				"qa datagen: %s token was unexpectedly accepted",
				probe.label,
			)
		}
		if !isAuthenticationRejection(err) {
			return loggedError(
				ctx,
				fmt.Sprintf(
					"qa datagen: %s token probe expected HTTP 401",
					probe.label,
				),
				err,
			)
		}
	}
	return nil
}

type authProbe struct {
	label string
	token string
}

func (g *Generator) now() time.Time {
	return g.content.ReferenceTime()
}
