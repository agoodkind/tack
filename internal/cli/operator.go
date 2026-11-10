package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// FlagOperatorSource resolves an operator from the global operator flags.
type FlagOperatorSource struct {
	// Factory provides the parsed global operator flags.
	Factory *Factory
}

// Resolve validates operator flag values and returns the asserted principal.
//
// The factory must provide a nonempty operator ID that parses as a UUID other
// than [uuid.Nil], and a nonempty email. Resolve trims the ID, email, and name
// before constructing the principal.
//
//	source := FlagOperatorSource{Factory: factory}
//	principal, err := source.Resolve(ctx)
func (s FlagOperatorSource) Resolve(ctx context.Context) (audit.OperatorPrincipal, error) {
	if s.Factory == nil {
		err := fmt.Errorf("operator flag source has no factory")
		slog.ErrorContext(ctx, "operator.flag.factory_missing", slog.String("err", err.Error()))

		return audit.OperatorPrincipal{}, err
	}

	operatorID, email, name := s.Factory.Operator()
	operatorID = strings.TrimSpace(operatorID)
	if operatorID == "" {
		err := fmt.Errorf("operator-id is required for flag identity")
		slog.ErrorContext(ctx, "operator.flag.id_missing", slog.String("err", err.Error()))

		return audit.OperatorPrincipal{}, err
	}

	id, err := uuid.Parse(operatorID)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"operator.flag.id_invalid",
			slog.String("operator_id", operatorID),
			slog.String("err", err.Error()),
		)

		return audit.OperatorPrincipal{}, fmt.Errorf("parse operator-id %q: %w", operatorID, err)
	}

	// The all-zero UUID parses successfully but does not identify an operator.
	// Reject it before an audit event can record an unattributable actor.
	if id == uuid.Nil {
		err := errors.New("operator-id is the nil UUID, which identifies nobody")
		slog.ErrorContext(ctx, "operator.flag.id_nil", slog.String("err", err.Error()))

		return audit.OperatorPrincipal{}, err
	}

	trimmedEmail := strings.TrimSpace(email)
	if trimmedEmail == "" {
		err := errors.New("operator-email is required alongside operator-id")
		slog.ErrorContext(ctx, "operator.flag.email_missing", slog.String("err", err.Error()))

		return audit.OperatorPrincipal{}, err
	}

	return audit.OperatorPrincipal{
		ID:     id,
		Email:  trimmedEmail,
		Name:   strings.TrimSpace(name),
		Source: "flag",
	}, nil
}

// NewOperatorSource creates the command line identity source.
//
// The root command constructs the source before Cobra parses flags. Resolve
// selects the service, flag, or Git configuration source after parsing, when
// the operator's selections are available.
//
//	source := NewOperatorSource(factory)
//	err := root.ParseFlags(args)
//	principal, resolveErr := source.Resolve(ctx)
func NewOperatorSource(f *Factory) audit.OperatorIdentitySource {
	return selectingOperatorSource{factory: f}
}

// selectingOperatorSource selects an identity source after flag parsing.
// A service name selects the service source. Without a service name, an
// operator ID selects the flag source; otherwise, Git configuration supplies
// the identity.
//
// A service name with an operator ID records the service as the actor and the
// flag operator as the accountable operator in OnBehalfOf. This pairing
// requires a session ID and a flag operator with a UUID other than [uuid.Nil]
// and a nonempty email. The selector rejects a session without a service name
// because only service identities record sessions.
type selectingOperatorSource struct {
	factory *Factory
}

func (s selectingOperatorSource) Resolve(ctx context.Context) (audit.OperatorPrincipal, error) {
	if s.factory == nil {
		return GitConfigOperatorSource{}.Resolve(ctx)
	}

	operatorID, _, _ := s.factory.Operator()
	hasOperatorID := strings.TrimSpace(operatorID) != ""
	hasServiceName := strings.TrimSpace(s.factory.OperatorService()) != ""
	hasSession := strings.TrimSpace(s.factory.OperatorSession()) != ""
	if hasSession && !hasServiceName {
		err := errors.New(
			"--operator-session requires --operator-service; only a service identity records a session",
		)
		slog.ErrorContext(
			ctx,
			"operator.select.session_without_service",
			slog.String("err", err.Error()),
		)

		return audit.OperatorPrincipal{}, err
	}

	if hasServiceName && hasOperatorID {
		return s.resolveServiceOnBehalfOf(ctx)
	}

	if hasServiceName {
		return ServiceOperatorSource{Factory: s.factory}.Resolve(ctx)
	}

	if hasOperatorID {
		return FlagOperatorSource{Factory: s.factory}.Resolve(ctx)
	}

	return GitConfigOperatorSource{}.Resolve(ctx)
}

// resolveServiceOnBehalfOf returns the service principal and records the flag
// operator as the accountable operator. This pairing does not use an act-as
// grant, and GrantID remains [uuid.Nil]. A command that accepts a reason fills
// Reason. The resolver rejects an empty session because the agent action must
// record its session.
func (s selectingOperatorSource) resolveServiceOnBehalfOf(
	ctx context.Context,
) (audit.OperatorPrincipal, error) {
	if strings.TrimSpace(s.factory.OperatorSession()) == "" {
		err := errors.New(
			"--operator-service with --operator-id requires --operator-session; an agent action records its session",
		)
		slog.ErrorContext(
			ctx,
			"operator.select.on_behalf_of_without_session",
			slog.String("err", err.Error()),
		)

		return audit.OperatorPrincipal{}, err
	}

	principal, err := ServiceOperatorSource{Factory: s.factory}.Resolve(ctx)
	if err != nil {
		return audit.OperatorPrincipal{}, err
	}

	accountable, err := FlagOperatorSource{Factory: s.factory}.Resolve(ctx)
	if err != nil {
		return audit.OperatorPrincipal{}, err
	}

	principal.OnBehalfOf = &audit.ActProvenance{
		OperatorID:    accountable.ID,
		OperatorEmail: accountable.Email,
		GrantID:       uuid.Nil,
		Reason:        "",
	}

	return principal, nil
}
