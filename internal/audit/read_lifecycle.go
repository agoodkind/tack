package audit

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const (
	// ReadRecoveryBuckets is the number of recovery buckets.
	ReadRecoveryBuckets = 64
	// ReadRecoveryScanLimit is the number of expired candidates in one scan.
	ReadRecoveryScanLimit = 32
	// ReadLease is the time a pending read stays owned after admission or renewal.
	ReadLease = 30 * time.Second
	// ReadRenewInterval is the time between two renewals of an active read.
	ReadRenewInterval = 10 * time.Second
	// ReadAdmissionRetryWindow is the time after the first admission during
	// which a retry of the same admission succeeds.
	ReadAdmissionRetryWindow = 30 * time.Second
	// ReadReceiptRetention is the time a published receipt stays after publication.
	ReadReceiptRetention = time.Hour
	// ReadCleanupBatch is the number of published receipts one cleanup deletes.
	ReadCleanupBatch = 256
	// ReadFirstGeneration is the generation of a new owner.
	ReadFirstGeneration uint64 = 1
	// readRecoveryErrorCode is the error code of an event that recovery saves.
	readRecoveryErrorCode = "read_owner_expired"
)

var (
	// ErrReadOwnerLost reports that the token does not own the pending read.
	ErrReadOwnerLost = errors.New("audit read: the token does not own the pending read")
	// ErrReadAdmissionExpired reports an admission retry after its deadline.
	ErrReadAdmissionExpired = errors.New("audit read: the admission retry deadline passed")
	// ErrReadIdentityChanged reports a final event that changes admitted fields.
	ErrReadIdentityChanged = errors.New("audit read: the final event changes admitted fields")
)

// ReadToken identifies one owner of one pending read.
type ReadToken struct {
	EventID, Owner    uuid.UUID
	Generation        uint64
	AdmissionDeadline time.Time
}

// ReadAdmission is the template event that BeginRead saves before the read.
type ReadAdmission struct {
	Token    ReadToken
	Template Event
}

// ReadCompletion is the final event, its ordered targets, and the transport
// digest that EncodeEvent computes from them.
type ReadCompletion struct {
	Token   ReadToken
	Event   Event
	Targets []Target
	Digest  string
}

// PublishedRead identifies one read event that Kafka acknowledged.
type PublishedRead struct {
	EventID uuid.UUID
	Digest  string
}

// ReadLifecycle stores pending reads and their terminal receipts.
type ReadLifecycle interface {
	BeginRead(ctx context.Context, admission ReadAdmission) (ReadToken, error)
	RenewRead(ctx context.Context, token ReadToken) error
	FinalizeRead(ctx context.Context, completion ReadCompletion) error
	RecoverExpired(ctx context.Context, bucket uint16, limit int) (int, error)
	AcknowledgePublished(ctx context.Context, reads []PublishedRead) error
	CleanupPublished(ctx context.Context, bucket uint16, limit int) (int, error)
}

// ReadBucket returns the first SHA-256 byte of the event ID modulo
// ReadRecoveryBuckets.
func ReadBucket(eventID uuid.UUID) uint16 {
	digest := sha256.Sum256(eventID[:])
	return uint16(digest[0]) % ReadRecoveryBuckets
}

// NewReadOwner returns a random owner ID for one admission.
func NewReadOwner() uuid.UUID {
	return uuid.New()
}

// ValidateReadAdmission checks that the template is a format 2 read event
// with the token's event ID, an owner, an org, an operation, and a start time.
func ValidateReadAdmission(admission ReadAdmission) error {
	template := admission.Template
	eventContext := template.Context
	switch {
	case admission.Token.EventID == uuid.Nil || admission.Token.Owner == uuid.Nil:
		return errors.New("audit read admission: event ID and owner are required")
	case template.EventID != admission.Token.EventID:
		return fmt.Errorf("audit read admission %s: template event ID differs from the token", admission.Token.EventID)
	case eventContext.AuditFormatVersion != AuditFormatComplete:
		return fmt.Errorf("audit read admission %s: template requires format 2", template.EventID)
	case eventContext.OperationClass != OperationRead || eventContext.Phase != PhaseRead:
		return fmt.Errorf("audit read admission %s: template requires the read class and phase", template.EventID)
	case eventContext.OrgID == uuid.Nil || eventContext.OperationID == uuid.Nil || eventContext.StartedAt.IsZero():
		return fmt.Errorf("audit read admission %s: template requires an org, an operation, and a start time", template.EventID)
	case eventContext.Target == nil:
		return fmt.Errorf("audit read admission %s: template requires a target", template.EventID)
	}
	return nil
}

// CheckReadCompletion verifies the final event against the admitted
// template, encodes it with its targets, and compares the transport digest.
// It rejects an incomplete outcome because only recovery saves one.
func CheckReadCompletion(template Event, completion ReadCompletion) (EncodedEvent, error) {
	final := completion.Event
	if !sameReadIdentity(template, final) {
		slog.Error("audit.read_lifecycle.identity_changed", slog.String("event_id", final.EventID.String()),
			slog.String("err", ErrReadIdentityChanged.Error()))
		return EncodedEvent{}, fmt.Errorf("check audit read %s: %w", final.EventID, ErrReadIdentityChanged)
	}
	if final.Outcome == OutcomeIncomplete {
		return EncodedEvent{}, fmt.Errorf("check audit read %s: only recovery saves the incomplete outcome", final.EventID)
	}
	if err := ValidateEvent(final); err != nil {
		return EncodedEvent{}, fmt.Errorf("check audit read %s: %s", final.EventID, err.Error())
	}
	encoded, err := EncodeEvent(final, completion.Targets)
	if err != nil {
		return EncodedEvent{}, err
	}
	if encoded.TransportDigest != completion.Digest {
		return EncodedEvent{}, fmt.Errorf("check audit read %s: digest differs from the encoded event", final.EventID)
	}
	return encoded, nil
}

// sameReadIdentity compares the actor, identity, and start fields that
// finalization must not change.
func sameReadIdentity(template, final Event) bool {
	templateContext, finalContext := template.Context, final.Context
	return template.EventID == final.EventID && template.Verb == final.Verb && template.Actor == final.Actor &&
		templateContext.OrgID == finalContext.OrgID && templateContext.OperationID == finalContext.OperationID &&
		templateContext.ParentOperationID == finalContext.ParentOperationID &&
		templateContext.StartedAt.Equal(finalContext.StartedAt) &&
		templateContext.AuditFormatVersion == finalContext.AuditFormatVersion &&
		templateContext.OperationClass == finalContext.OperationClass && templateContext.Phase == finalContext.Phase
}

// ConfirmsReadCommit reports whether a terminal receipt confirms an earlier
// finalization by the same owner with the same digest.
func ConfirmsReadCommit(receiptOwner uuid.UUID, receiptDigest string, receiptOutcome Outcome, completion ReadCompletion) bool {
	return receiptOwner == completion.Token.Owner && receiptDigest == completion.Digest &&
		receiptOutcome != OutcomeIncomplete
}
