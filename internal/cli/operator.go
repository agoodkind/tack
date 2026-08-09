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

// Resolve parses the operator flags and returns the asserted principal.
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
		slog.ErrorContext(ctx, "operator.flag.id_invalid",
			slog.String("operator_id", operatorID), slog.String("err", err.Error()))
		return audit.OperatorPrincipal{}, fmt.Errorf("parse operator-id %q: %w", operatorID, err)
	}
	// The all-zeros UUID parses cleanly and names nobody. Recording it would
	// put a row in the ledger with no attributable actor, which is the one
	// thing an operator identity exists to prevent.
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

// NewOperatorSource returns the identity source for the command line. It
// chooses between the flags and the local git config when Resolve is called,
// not when it is constructed. The root command is built before cobra parses
// anything, so a choice made at construction would always see empty flags and
// would silently ignore an operator who passed one.
func NewOperatorSource(f *Factory) audit.OperatorIdentitySource {
	return selectingOperatorSource{factory: f}
}

// selectingOperatorSource picks the flag source when the operator supplied an
// id, and the git config source otherwise.
type selectingOperatorSource struct {
	factory *Factory
}

func (s selectingOperatorSource) Resolve(ctx context.Context) (audit.OperatorPrincipal, error) {
	if s.factory != nil {
		operatorID, _, _ := s.factory.Operator()
		if strings.TrimSpace(operatorID) != "" {
			return FlagOperatorSource{Factory: s.factory}.Resolve(ctx)
		}
	}
	return GitConfigOperatorSource{}.Resolve(ctx)
}
