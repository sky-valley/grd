package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const maxConflictPaths = 256
const maxConflictPathBytes = 4096
const maxConflictPathsTotalBytes = 48 * 1024

var ErrVersionNotPromoted = errors.New("change version is not promoted")
var ErrReconciliationConflictNotFound = errors.New("reconciliation conflict not found")
var ErrInvalidReconciliationConflict = errors.New("invalid reconciliation conflict")
var ErrInvalidReconciliationLineage = errors.New("invalid reconciliation lineage")

type ConflictID string

type EffectiveReconciliationTransitionKind string

const (
	AmendmentTransition         EffectiveReconciliationTransitionKind = "amendment"
	HeldVersionRebaseTransition EffectiveReconciliationTransitionKind = "held_version_rebase"
)

type EffectiveReconciliationTransition struct {
	Kind        EffectiveReconciliationTransitionKind
	FromVersion VersionID
	ToVersion   VersionID
	FromIntent  RevisionID
	ToIntent    RevisionID
	Rationale   string
}

type ReconciliationConflict struct {
	ID            ConflictID
	Change        Change
	Version       Version
	FromVersion   VersionID
	ToVersion     VersionID
	BaseIntent    RevisionID
	ReportedBy    string
	AffectedPaths []string
}

type ReconciliationConflictInspection struct {
	ReconciliationConflict
	Resolution           *ReconciliationResolution
	EffectiveVersion     *Version
	EffectiveTransitions []EffectiveReconciliationTransition
	Superseded           bool
}

type ReconciliationConflictRequest struct {
	IdempotencyKey    string
	FromVersion       VersionID
	ToVersion         VersionID
	DescendantVersion VersionID
	ExpectedIntent    RevisionID
	ReportedBy        string
	AffectedPaths     []string
}

type ReconciliationConflictQuery struct {
	After ConflictID
	Limit int
}

type ReconciliationConflictPage struct {
	Conflicts  []ReconciliationConflictInspection
	NextCursor ConflictID
}

func (repository *Repository) RecordReconciliationConflict(ctx context.Context, request ReconciliationConflictRequest) (ReconciliationConflictInspection, error) {
	if request.IdempotencyKey == "" {
		return ReconciliationConflictInspection{}, fmt.Errorf("%w: idempotency key is required", ErrInvalidReconciliationConflict)
	}
	if request.FromVersion == "" || request.ToVersion == "" || request.DescendantVersion == "" || request.ExpectedIntent == "" {
		return ReconciliationConflictInspection{}, fmt.Errorf("%w: versions and expected intent are required", ErrInvalidReconciliationConflict)
	}
	if request.ReportedBy == "" {
		return ReconciliationConflictInspection{}, fmt.Errorf("%w: reporter is required", ErrInvalidReconciliationConflict)
	}
	paths, err := NormalizeReconciliationConflictPaths(request.AffectedPaths)
	if err != nil {
		return ReconciliationConflictInspection{}, err
	}

	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()
	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()

	existing, found, err := repository.conflicts.ReconciliationConflictByIdempotencyKey(ctx, request.IdempotencyKey)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read reconciliation conflict idempotency record: %w", err)
	}
	if found {
		if existing.FromVersion != request.FromVersion ||
			existing.ToVersion != request.ToVersion ||
			existing.Version.ID != request.DescendantVersion ||
			existing.BaseIntent != request.ExpectedIntent ||
			existing.ReportedBy != request.ReportedBy {
			return ReconciliationConflictInspection{}, ErrIdempotencyConflict
		}
		return repository.deriveReconciliationConflictState(ctx, existing)
	}

	from, found, err := repository.changes.Version(ctx, request.FromVersion)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read reconciliation source version: %w", err)
	}
	if !found {
		return ReconciliationConflictInspection{}, ErrVersionNotFound
	}
	to, found, err := repository.changes.Version(ctx, request.ToVersion)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read reconciliation target version: %w", err)
	}
	if !found {
		return ReconciliationConflictInspection{}, ErrVersionNotFound
	}
	if from.ChangeID != to.ChangeID {
		return ReconciliationConflictInspection{}, fmt.Errorf("%w: versions belong to different changes", ErrInvalidReconciliationLineage)
	}
	latest, found, err := repository.changes.LatestVersion(ctx, from.ChangeID)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read latest reconciled version: %w", err)
	}
	if !found || latest.ID != to.ID {
		return ReconciliationConflictInspection{}, ErrVersionAdvanced
	}
	related, err := repository.isAmendmentDescendant(ctx, from.ID, to.ID)
	if err != nil {
		return ReconciliationConflictInspection{}, err
	}
	if !related {
		return ReconciliationConflictInspection{}, fmt.Errorf("%w: versions are not an amendment lineage", ErrInvalidReconciliationLineage)
	}
	promoted, found, err := repository.promotions.CompletedPromotion(ctx, to.ID)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read reconciliation target promotion: %w", err)
	}
	if !found {
		return ReconciliationConflictInspection{}, ErrVersionNotPromoted
	}
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read current intent for reconciliation conflict: %w", err)
	}
	if !found || current.ID != request.ExpectedIntent {
		return ReconciliationConflictInspection{}, ErrIntentAdvanced
	}
	acceptedInHistory, err := repository.intentDescendsFrom(ctx, current, promoted.Intent.ID)
	if err != nil {
		return ReconciliationConflictInspection{}, err
	}
	if !acceptedInHistory {
		return ReconciliationConflictInspection{}, ErrIntentAdvanced
	}
	descendant, found, err := repository.changes.Version(ctx, request.DescendantVersion)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read reconciliation descendant version: %w", err)
	}
	if !found {
		return ReconciliationConflictInspection{}, ErrVersionNotFound
	}
	latestDescendant, found, err := repository.changes.LatestVersion(ctx, descendant.ChangeID)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read latest reconciliation descendant version: %w", err)
	}
	if !found || latestDescendant.ID != descendant.ID {
		return ReconciliationConflictInspection{}, ErrVersionAdvanced
	}
	if descendant.ChangeID == from.ChangeID || descendant.BaseIntent != from.BaseIntent {
		return ReconciliationConflictInspection{}, fmt.Errorf("%w: descendant does not identify separate work from the reconciled base", ErrInvalidReconciliationLineage)
	}
	descendantChange, found, err := repository.changes.Change(ctx, descendant.ChangeID)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read reconciliation descendant change: %w", err)
	}
	if !found {
		return ReconciliationConflictInspection{}, ErrChangeNotFound
	}

	conflictID, err := newID("conflict")
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("create reconciliation conflict id: %w", err)
	}
	conflict := ReconciliationConflict{
		ID:            ConflictID(conflictID),
		Change:        descendantChange,
		Version:       descendant,
		FromVersion:   from.ID,
		ToVersion:     to.ID,
		BaseIntent:    current.ID,
		ReportedBy:    request.ReportedBy,
		AffectedPaths: paths,
	}
	if err := repository.conflicts.RecordReconciliationConflict(ctx, request.IdempotencyKey, conflict); err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("record reconciliation conflict: %w", err)
	}
	return ReconciliationConflictInspection{ReconciliationConflict: cloneReconciliationConflict(conflict)}, nil
}

func (repository *Repository) ReconciliationConflict(ctx context.Context, id ConflictID) (ReconciliationConflictInspection, bool, error) {
	conflict, found, err := repository.conflicts.ReconciliationConflict(ctx, id)
	if err != nil {
		return ReconciliationConflictInspection{}, false, fmt.Errorf("read reconciliation conflict: %w", err)
	}
	if !found {
		return ReconciliationConflictInspection{}, false, nil
	}
	derived, err := repository.deriveReconciliationConflictState(ctx, conflict)
	return derived, true, err
}

func (repository *Repository) ReconciliationConflicts(ctx context.Context, query ReconciliationConflictQuery) (ReconciliationConflictPage, error) {
	if query.Limit < 1 || query.Limit > 100 {
		return ReconciliationConflictPage{}, errors.New("reconciliation conflict page limit must be between 1 and 100")
	}
	conflicts, more, err := repository.conflicts.ReconciliationConflicts(ctx, query.After, query.Limit)
	if err != nil {
		return ReconciliationConflictPage{}, fmt.Errorf("read reconciliation conflicts: %w", err)
	}
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return ReconciliationConflictPage{}, fmt.Errorf("read current intent for reconciliation conflicts: %w", err)
	}
	if !found {
		return ReconciliationConflictPage{}, errors.New("intent ledger is not initialized")
	}
	for index := range conflicts {
		derived, err := repository.deriveReconciliationConflictAgainstCurrent(ctx, conflicts[index], current)
		if err != nil {
			return ReconciliationConflictPage{}, err
		}
		conflicts[index] = derived
	}
	page := ReconciliationConflictPage{Conflicts: conflicts}
	if more && len(conflicts) > 0 {
		page.NextCursor = conflicts[len(conflicts)-1].ID
	}
	return page, nil
}

func (repository *Repository) deriveReconciliationConflictState(
	ctx context.Context,
	inspection ReconciliationConflictInspection,
) (ReconciliationConflictInspection, error) {
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read current intent for reconciliation conflict: %w", err)
	}
	if !found {
		return ReconciliationConflictInspection{}, errors.New("intent ledger is not initialized")
	}
	inspection = cloneReconciliationConflictInspection(inspection)
	inspection, err = repository.deriveReconciliationConflictAgainstCurrent(ctx, inspection, current)
	if err != nil {
		return ReconciliationConflictInspection{}, err
	}
	return inspection, nil
}

func (repository *Repository) deriveReconciliationConflictAgainstCurrent(
	ctx context.Context,
	inspection ReconciliationConflictInspection,
	current Revision,
) (ReconciliationConflictInspection, error) {
	if inspection.Resolution == nil {
		inspection.Superseded = inspection.BaseIntent != current.ID
		return inspection, nil
	}
	effective, transitions, err := repository.effectiveReconciliationVersion(ctx, inspection)
	if err != nil {
		return ReconciliationConflictInspection{}, err
	}
	inspection.EffectiveVersion = &effective
	inspection.EffectiveTransitions = transitions
	if _, promoted, err := repository.promotions.CompletedPromotion(ctx, effective.ID); err != nil {
		return ReconciliationConflictInspection{}, fmt.Errorf("read effective reconciliation promotion: %w", err)
	} else if promoted {
		inspection.Superseded = false
		return inspection, nil
	}
	inspection.Superseded = effective.BaseIntent != current.ID
	return inspection, nil
}

func (repository *Repository) effectiveReconciliationVersion(
	ctx context.Context,
	inspection ReconciliationConflictInspection,
) (Version, []EffectiveReconciliationTransition, error) {
	resolved, found, err := repository.changes.Version(ctx, inspection.Resolution.ToVersion)
	if err != nil {
		return Version{}, nil, fmt.Errorf("read reconciliation resolution version: %w", err)
	}
	if !found {
		return Version{}, nil, errors.New("reconciliation resolution version is not recorded")
	}
	latest, found, err := repository.changes.LatestVersion(ctx, inspection.Change.ID)
	if err != nil {
		return Version{}, nil, fmt.Errorf("read latest reconciliation resolution version: %w", err)
	}
	if !found {
		return Version{}, nil, errors.New("reconciliation conflict change has no recorded version")
	}
	if latest.ID == resolved.ID {
		return latest, nil, nil
	}

	var reversed []EffectiveReconciliationTransition
	seen := make(map[VersionID]struct{})
	for current := latest.ID; current != resolved.ID; {
		if _, duplicate := seen[current]; duplicate {
			return Version{}, nil, errors.New("held version rebase lineage contains a cycle")
		}
		seen[current] = struct{}{}
		to, found, err := repository.changes.Version(ctx, current)
		if err != nil {
			return Version{}, nil, fmt.Errorf("read effective transition target version: %w", err)
		}
		if !found {
			return Version{}, nil, errors.New("effective transition target version is not recorded")
		}
		rebase, found, err := repository.rebases.HeldVersionRebase(ctx, current)
		if err != nil {
			return Version{}, nil, fmt.Errorf("read effective held version rebase: %w", err)
		}
		if found {
			reversed = append(reversed, EffectiveReconciliationTransition{
				Kind:        HeldVersionRebaseTransition,
				FromVersion: rebase.FromVersion,
				ToVersion:   rebase.ToVersion,
				FromIntent:  rebase.FromIntent,
				ToIntent:    rebase.ToIntent,
				Rationale:   rebase.Rationale,
			})
			current = rebase.FromVersion
			continue
		}
		amendment, found, err := repository.amendments.Amendment(ctx, current)
		if err != nil {
			return Version{}, nil, fmt.Errorf("read effective amendment: %w", err)
		}
		if !found {
			// A later transition of an unknown kind is not part of this
			// resolution's effective lineage. Preserve the immutable resolution.
			return resolved, nil, nil
		}
		from, found, err := repository.changes.Version(ctx, amendment.FromVersion)
		if err != nil {
			return Version{}, nil, fmt.Errorf("read effective amendment source version: %w", err)
		}
		if !found {
			return Version{}, nil, errors.New("effective amendment source version is not recorded")
		}
		reversed = append(reversed, EffectiveReconciliationTransition{
			Kind:        AmendmentTransition,
			FromVersion: amendment.FromVersion,
			ToVersion:   amendment.ToVersion,
			FromIntent:  from.BaseIntent,
			ToIntent:    to.BaseIntent,
			Rationale:   amendment.Rationale,
		})
		current = amendment.FromVersion
	}
	transitions := make([]EffectiveReconciliationTransition, len(reversed))
	for index := range reversed {
		transitions[len(reversed)-1-index] = reversed[index]
	}
	return latest, transitions, nil
}

func NormalizeReconciliationConflictPaths(paths []string) ([]string, error) {
	if len(paths) > maxConflictPaths {
		return nil, fmt.Errorf("%w: affected paths must be bounded", ErrInvalidReconciliationConflict)
	}
	seen := make(map[string]struct{}, len(paths))
	normalized := make([]string, 0, len(paths))
	totalBytes := 0
	for _, path := range paths {
		if path == "" || len(path) > maxConflictPathBytes || strings.ContainsRune(path, '\x00') {
			return nil, fmt.Errorf("%w: affected path is invalid", ErrInvalidReconciliationConflict)
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		totalBytes += len(path)
		if totalBytes > maxConflictPathsTotalBytes {
			return nil, fmt.Errorf("%w: affected paths exceed the aggregate bound", ErrInvalidReconciliationConflict)
		}
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func cloneReconciliationConflict(conflict ReconciliationConflict) ReconciliationConflict {
	conflict.Version = cloneVersion(conflict.Version)
	conflict.AffectedPaths = slices.Clone(conflict.AffectedPaths)
	return conflict
}

func cloneReconciliationConflictInspection(inspection ReconciliationConflictInspection) ReconciliationConflictInspection {
	inspection.ReconciliationConflict = cloneReconciliationConflict(inspection.ReconciliationConflict)
	if inspection.Resolution != nil {
		resolution := *inspection.Resolution
		inspection.Resolution = &resolution
	}
	if inspection.EffectiveVersion != nil {
		effective := cloneVersion(*inspection.EffectiveVersion)
		inspection.EffectiveVersion = &effective
	}
	inspection.EffectiveTransitions = slices.Clone(inspection.EffectiveTransitions)
	return inspection
}
