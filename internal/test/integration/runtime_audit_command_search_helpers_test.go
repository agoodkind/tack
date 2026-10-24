package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// requireSearchPreviewRead runs the dry run of a search command as a new
// operator. It requires one completed read event for verb with the digest
// and format of the JSON result that the dry run printed.
func requireSearchPreviewRead(t *testing.T, cfg *config.Config, pool *pgxpool.Pool, verb audit.Verb, command ...string) {
	t.Helper()
	reader := uuid.Must(uuid.NewV7())
	arguments := append([]string{"--output", "json", "--operator-id", reader.String(), "--operator-email", outcomeOperatorEmail}, command...)
	output, err := runOpsCommand(t, cfg, pool, arguments...)
	if err != nil {
		t.Fatalf("the dry run of %v failed: %v\n%s", command, err, output)
	}
	digest := payloadDigest(printedPayload(t, []byte(output)))
	reads := readOutboxEvents(t, pool, verb, reader)
	if len(reads) != 1 || reads[0].Outcome != audit.OutcomeCompleted || reads[0].Context.OperationClass != audit.OperationRead {
		t.Fatalf("the dry run of %v recorded %+v instead of one completed read", command, reads)
	}
	evidence := reads[0].Context.Read
	if evidence == nil || evidence.PayloadSHA256 != digest || evidence.PayloadFormat != "application/json" {
		t.Fatalf("the dry run of %v recorded read evidence %+v instead of the JSON digest %s of the printed result", command, evidence, digest)
	}
}

// storedRolloutInitiation reads the access rollout of authority and returns
// the initiation that its record stores.
func storedRolloutInitiation(t *testing.T, clusterFile string, authority uuid.UUID) searchdomain.Initiation {
	t.Helper()
	stores, err := fdbadapter.NewStores(clusterFile, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("opening FoundationDB stores failed: %v", err)
	}
	rollout, err := stores.SearchRollouts(clock.Wall{}, stores.SearchPolicySet()).Current(t.Context(), authority)
	if err != nil {
		t.Fatalf("reading the access rollout of authority %s failed: %v", authority, err)
	}
	return rollout.Initiation
}

// requireInitiation requires a search record to store the operation and
// operator of the command that began it.
func requireInitiation(t *testing.T, record string, stored searchdomain.Initiation, operationID, operatorID uuid.UUID) {
	t.Helper()
	if stored.OperationID != operationID || stored.ActorID != operatorID {
		t.Fatalf("the %s stores initiation %+v instead of operation %s and operator %s", record, stored, operationID, operatorID)
	}
}
