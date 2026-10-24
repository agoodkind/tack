// Package service implements node, property, search, and seed operations
// for MCP tools, RPC handlers, and operator commands.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// FixedDefinitionStore scans stored property definitions in bounded pages
// and replaces a definition only if it still matches the scanned record.
type FixedDefinitionStore interface {
	ScanProjections(ctx context.Context, cursor string) ([]*node.PropertyDef, string, error)
	Replace(ctx context.Context, before, after *node.PropertyDef) (bool, error)
}

// FixedDefinitionAudit stages an audit event for the correction from
// before to after on the context it returns.
type FixedDefinitionAudit func(ctx context.Context, before, after *node.PropertyDef) (context.Context, error)

// FixedDefinitionResult counts scanned, corrected, unchanged, and
// conflicted definitions. A dry run counts definitions that need correction
// as corrected. A conflicted definition changed after the scan read it.
// Changes lists each corrected or conflicted definition's org ID, property
// definition ID, name, outcome, and before and after values only for fields
// that need correction. Dry runs and execute runs both populate Changes.
type FixedDefinitionResult struct {
	Scanned       int                       `json:"scanned"`
	Corrected     int                       `json:"corrected"`
	Unchanged     int                       `json:"unchanged"`
	Conflicted    int                       `json:"conflicted"`
	CorrectedIDs  []node.ProjectionIdentity `json:"corrected_ids,omitempty"`
	ConflictedIDs []node.ProjectionIdentity `json:"conflicted_ids,omitempty"`
	Changes       []FixedDefinitionChange   `json:"changes,omitempty"`
}

// CorrectFixedDefinitions uses ScanProjections to page through every
// stored property definition in every organization. It corrects definitions
// that have a seeded built-in ID. When the seed excludes the property from
// search, it writes that exclusion if the stored definition includes the
// property or lacks a search declaration. It writes the seeded
// AppliesToFeatures when the seeded list is empty, or when the stored list
// is not empty and every stored feature appears in the seeded list.
// A dry run does not write. For each correction, it calls stage and uses
// store.Replace to commit the definition and audit event only if the stored
// definition still matches the scanned record.
func CorrectFixedDefinitions(ctx context.Context, store FixedDefinitionStore, stage FixedDefinitionAudit, dryRun bool) (FixedDefinitionResult, error) {
	result := FixedDefinitionResult{Scanned: 0, Corrected: 0, Unchanged: 0, Conflicted: 0, CorrectedIDs: nil, ConflictedIDs: nil, Changes: nil}
	seeded := seededDefinitions{orgID: uuid.Nil, byID: nil}
	cursor := ""
	for {
		definitions, next, err := store.ScanProjections(ctx, cursor)
		if err != nil {
			return result, fixedDefinitionFailure(ctx, "scan property definitions after cursor "+cursor, err)
		}
		for _, stored := range definitions {
			seed, err := seeded.lookup(stored.OrgID, stored.ID)
			if err != nil {
				return result, fixedDefinitionFailure(ctx, "read seeded definitions of organization "+stored.OrgID.String(), err)
			}
			if err := correctDefinition(ctx, store, stage, stored, seed, dryRun, &result); err != nil {
				return result, err
			}
		}
		if next == "" {
			return result, nil
		}
		if next == cursor {
			return result, fixedDefinitionFailure(ctx, "scan property definitions", errors.New("the scan cursor did not advance"))
		}
		cursor = next
	}
}

// correctDefinition counts the outcome for one stored definition. It
// applies the correction unless dryRun is true.
func correctDefinition(ctx context.Context, store FixedDefinitionStore, stage FixedDefinitionAudit, stored, seed *node.PropertyDef, dryRun bool, result *FixedDefinitionResult) error {
	result.Scanned++
	corrected := correctedDefinition(stored, seed)
	identity := node.ProjectionIdentity{OrgID: stored.OrgID, PropertyDefID: stored.ID}
	if corrected == nil {
		result.Unchanged++
		return nil
	}
	if dryRun {
		result.Corrected++
		result.CorrectedIDs = append(result.CorrectedIDs, identity)
		result.Changes = append(result.Changes, definitionChange(fixedOutcomeCorrected, stored, corrected))
		return nil
	}
	staged, err := stage(ctx, stored, corrected)
	if err != nil {
		return fixedDefinitionFailure(ctx, "stage the correction audit of definition "+stored.ID.String(), err)
	}
	replaced, err := store.Replace(staged, stored, corrected)
	if err != nil {
		return fixedDefinitionFailure(ctx, "correct definition "+stored.ID.String(), err)
	}
	if !replaced {
		result.Conflicted++
		result.ConflictedIDs = append(result.ConflictedIDs, identity)
		result.Changes = append(result.Changes, definitionChange(fixedOutcomeConflicted, stored, corrected))
		return nil
	}
	result.Corrected++
	result.CorrectedIDs = append(result.CorrectedIDs, identity)
	result.Changes = append(result.Changes, definitionChange(fixedOutcomeCorrected, stored, corrected))
	telemetry.L(ctx).InfoContext(ctx, "metadata.fixed_definition.corrected", slog.String("org_id", stored.OrgID.String()),
		slog.String("property_definition_id", stored.ID.String()), slog.String("name", stored.Name))
	return nil
}

// correctedDefinition returns a copy of stored with the seeded
// corrections. It returns nil when seed is nil or the stored definition
// does not need correction.
func correctedDefinition(stored, seed *node.PropertyDef) *node.PropertyDef {
	if seed == nil {
		return nil
	}
	next := *stored
	changed := false
	if seed.Search != nil && !seed.Search.Include && (stored.Search == nil || stored.Search.Include) {
		exclusion := *seed.Search
		next.Search = &exclusion
		changed = true
	}
	if widensApplicability(stored.AppliesToFeatures, seed.AppliesToFeatures) && !sameFeatures(stored.AppliesToFeatures, seed.AppliesToFeatures) {
		next.AppliesToFeatures = slices.Clone(seed.AppliesToFeatures)
		changed = true
	}
	if !changed {
		return nil
	}
	return &next
}

// widensApplicability returns true when seeded is empty, or when stored
// is not empty and every feature in stored appears in seeded.
func widensApplicability(stored, seeded []string) bool {
	if len(seeded) == 0 {
		return true
	}
	if len(stored) == 0 {
		return false
	}
	for _, feature := range stored {
		if !slices.Contains(seeded, feature) {
			return false
		}
	}
	return true
}

func sameFeatures(left, right []string) bool {
	sortedLeft, sortedRight := slices.Clone(left), slices.Clone(right)
	slices.Sort(sortedLeft)
	slices.Sort(sortedRight)
	return slices.Equal(sortedLeft, sortedRight)
}

// seededDefinitions caches one organization's seeded definitions by ID.
// The cache replaces its definitions when lookup requests a different
// organization.
type seededDefinitions struct {
	orgID uuid.UUID
	byID  map[uuid.UUID]*node.PropertyDef
}

func (s *seededDefinitions) lookup(orgID, definitionID uuid.UUID) (*node.PropertyDef, error) {
	if s.byID == nil || s.orgID != orgID {
		definitions, err := defaultPropertyDefs(orgID)
		if err != nil {
			return nil, err
		}
		s.orgID, s.byID = orgID, make(map[uuid.UUID]*node.PropertyDef, len(definitions))
		for _, definition := range definitions {
			s.byID[definition.ID] = definition
		}
	}
	return s.byID[definitionID], nil
}

func fixedDefinitionFailure(ctx context.Context, operation string, err error) error {
	slog.ErrorContext(ctx, "metadata.fixed_definition.correction_failed", slog.String("err", err.Error()))
	return fmt.Errorf("correct fixed definitions: %s: %w", operation, err)
}
