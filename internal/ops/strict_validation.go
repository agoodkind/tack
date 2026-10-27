package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

const (
	// strictScanPageSize is the number of node records that one scan page
	// reads.
	strictScanPageSize = 100
	// maxStrictPasses limits rescans when the scan evidence changes.
	maxStrictPasses = 5
	// maxReportedViolations limits the number of violations in the report.
	maxReportedViolations = 20
)

var errStrictViolations = errors.New("the strict validation scan found invalid node records")

// StrictValidationStore reads write fence evidence and node records and
// writes the activation record.
type StrictValidationStore interface {
	StrictActivation(ctx context.Context) (node.StrictActivation, error)
	CaptureStrictEvidence(ctx context.Context) (node.StrictEvidence, error)
	StrictScanPage(ctx context.Context, cursor string, limit int) ([]node.StrictRecord, string, error)
	ActivateStrict(ctx context.Context, evidence node.StrictEvidence, activation node.StrictActivation) (node.StrictEvidenceChange, error)
}

// StrictViolation identifies a node record that its organization's schema
// rejects.
type StrictViolation struct {
	OrgID    uuid.UUID `json:"org_id"`
	NodeID   uuid.UUID `json:"node_id"`
	NodeType string    `json:"node_type"`
	Reason   string    `json:"reason"`
}

// StrictValidationReport is the result of one strict validation run.
type StrictValidationReport struct {
	AlreadyActive    bool              `json:"already_active"`
	Activated        bool              `json:"activated"`
	ScanID           uuid.UUID         `json:"scan_id"`
	RecordsValidated int               `json:"records_validated"`
	Passes           int               `json:"passes"`
	ViolationCount   int               `json:"violation_count"`
	FirstViolations  []StrictViolation `json:"first_violations"`
}

type strictScan struct {
	store        StrictValidationStore
	schemas      storeSchemaSource
	evidence     node.StrictEvidence
	violations   map[uuid.UUID]StrictViolation
	cache        map[uuid.UUID]node.Schema
	validated    map[uuid.UUID]uuid.UUID
	dependencies map[uuid.UUID][]uuid.UUID
}

// RunStrictValidation captures evidence and validates every stored node
// record. When the evidence changes, it repeats validation for the affected
// buckets and organizations until the evidence stops changing. When execute
// is false, it returns the report without an error, including the invalid
// records. When execute is true, it returns an error while any record is
// invalid and does not activate strict validation. Otherwise, it activates
// strict validation in one transaction that compares the evidence again.
func RunStrictValidation(ctx context.Context, store StrictValidationStore, schemas storeSchemaSource, execute bool) (StrictValidationReport, error) {
	report := StrictValidationReport{AlreadyActive: false, Activated: false, ScanID: uuid.Nil, RecordsValidated: 0, Passes: 0, ViolationCount: 0, FirstViolations: nil}
	current, err := store.StrictActivation(ctx)
	if err != nil {
		return report, strictFailure(ctx, "read the strict validation activation record", err)
	}
	if current.Active {
		report.AlreadyActive, report.ScanID, report.RecordsValidated = true, current.ScanID, current.RecordsValidated
		return report, nil
	}
	report.ScanID, err = uuid.NewV7()
	if err != nil {
		return report, strictFailure(ctx, "create the strict validation scan ID", err)
	}
	evidence, err := store.CaptureStrictEvidence(ctx)
	if err != nil {
		return report, strictFailure(ctx, "capture the strict validation evidence", err)
	}
	scan := &strictScan{
		store: store, schemas: schemas, evidence: evidence,
		violations: map[uuid.UUID]StrictViolation{}, cache: map[uuid.UUID]node.Schema{}, validated: map[uuid.UUID]uuid.UUID{},
		dependencies: map[uuid.UUID][]uuid.UUID{},
	}
	change := node.StrictEvidenceChange{Buckets: nil, Organizations: nil}
	for report.Passes = 1; report.Passes <= maxStrictPasses; report.Passes++ {
		if err := scan.pass(ctx, change, report.Passes == 1); err != nil {
			return report, err
		}
		change, err = scan.recapture(ctx)
		if err != nil {
			return report, err
		}
		if !change.Empty() {
			continue
		}
		report.RecordsValidated = len(scan.validated)
		scan.fill(&report)
		if !execute {
			return report, nil
		}
		if report.ViolationCount > 0 {
			return report, strictFailure(ctx, "refuse strict validation activation", fmt.Errorf("%d invalid records: %w", report.ViolationCount, errStrictViolations))
		}
		activation := node.StrictActivation{Active: true, ActivatedAt: clock.Now().UTC(), ScanID: report.ScanID, RecordsValidated: report.RecordsValidated}
		_, err = store.ActivateStrict(ctx, scan.evidence, activation)
		if errors.Is(err, node.ErrStrictEvidenceChanged) {
			change, err = scan.recapture(ctx)
			if err != nil {
				return report, err
			}
			continue
		}
		if err != nil {
			return report, strictFailure(ctx, "activate strict validation", err)
		}
		report.Activated = true
		return report, nil
	}
	return report, strictFailure(ctx, "complete the strict validation scan", fmt.Errorf("evidence changed in each of %d passes: %w", maxStrictPasses, node.ErrStrictEvidenceChanged))
}

// recapture captures fresh evidence and returns the changes since the
// evidence for the last pass. It stores the fresh evidence for the next pass.
func (s *strictScan) recapture(ctx context.Context) (node.StrictEvidenceChange, error) {
	later, err := s.store.CaptureStrictEvidence(ctx)
	if err != nil {
		return node.StrictEvidenceChange{Buckets: nil, Organizations: nil}, strictFailure(ctx, "capture the strict validation evidence", err)
	}
	change := s.evidence.Compare(later)
	s.evidence = later
	for _, orgID := range change.Organizations {
		delete(s.cache, orgID)
	}
	return change, nil
}

func (s *strictScan) fill(report *StrictValidationReport) {
	report.ViolationCount = len(s.violations)
	all := make([]StrictViolation, 0, len(s.violations))
	for _, violation := range s.violations {
		all = append(all, violation)
	}
	slices.SortFunc(all, compareStrictViolation)
	report.FirstViolations = all[:min(len(all), maxReportedViolations)]
}

func strictFailure(ctx context.Context, operation string, err error) error {
	slog.ErrorContext(ctx, "strict.validation_failed", slog.String("operation", operation), slog.String("err", err.Error()))
	return fmt.Errorf("%s: %w", operation, err)
}
