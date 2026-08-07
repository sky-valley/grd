package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

type DependentReconciliationCandidate struct {
	ReplacedDependency VersionID
	AcceptedVersion    VersionID
	AcceptedAtIntent   RevisionID
	Dependent          Proposed
}

type dependentReconciliationConflictKey struct {
	ReplacedDependency VersionID
	AcceptedVersion    VersionID
	DependentVersion   VersionID
	BaseIntent         RevisionID
}

// DependentReconciliations derives the latest unpromoted changes that still
// depend on the superseded source of the amendment which produced current
// intent. It is a read over immutable history, not a second work queue.
func (repository *Repository) DependentReconciliations(ctx context.Context) ([]DependentReconciliationCandidate, error) {
	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()
	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()

	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current intent: %w", err)
	}
	if !found {
		return nil, errors.New("intent ledger is not initialized")
	}
	conflicts, err := repository.dependentReconciliationConflicts(ctx)
	if err != nil {
		return nil, err
	}

	var candidates []DependentReconciliationCandidate
	seenChanges := make(map[ChangeID]struct{})
	revision := current
	for {
		promoted, promotedFound, err := repository.promotions.CompletedPromotionByIntent(ctx, revision.ID)
		if err != nil {
			return nil, fmt.Errorf("read accepted intent promotion: %w", err)
		}
		if promotedFound {
			acceptedVersion := promoted.Promotion.VersionID
			for toVersion := acceptedVersion; ; {
				amendment, amendmentFound, err := repository.amendments.Amendment(ctx, toVersion)
				if err != nil {
					return nil, fmt.Errorf("read accepted amendment history: %w", err)
				}
				if !amendmentFound {
					break
				}
				candidates, err = repository.appendDependentReconciliations(
					ctx,
					candidates,
					seenChanges,
					conflicts,
					amendment.FromVersion,
					acceptedVersion,
					revision.ID,
					current.ID,
				)
				if err != nil {
					return nil, err
				}
				toVersion = amendment.FromVersion
			}
		}
		if revision.PreviousID == "" {
			break
		}
		revision, found, err = repository.intents.Revision(ctx, revision.PreviousID)
		if err != nil {
			return nil, fmt.Errorf("read accepted intent history: %w", err)
		}
		if !found {
			return nil, errors.New("accepted intent history is incomplete")
		}
	}
	return candidates, nil
}

func (repository *Repository) appendDependentReconciliations(
	ctx context.Context,
	candidates []DependentReconciliationCandidate,
	seenChanges map[ChangeID]struct{},
	conflicts map[dependentReconciliationConflictKey]struct{},
	fromVersion VersionID,
	toVersion VersionID,
	baseIntent RevisionID,
	currentIntent RevisionID,
) ([]DependentReconciliationCandidate, error) {
	dependents, err := repository.changes.Dependents(ctx, fromVersion)
	if err != nil {
		return nil, fmt.Errorf("read superseded version dependents: %w", err)
	}
	for _, dependent := range dependents {
		if _, seen := seenChanges[dependent.ChangeID]; seen {
			continue
		}
		latest, found, err := repository.changes.LatestVersion(ctx, dependent.ChangeID)
		if err != nil {
			return nil, fmt.Errorf("read latest dependent version: %w", err)
		}
		if !found {
			return nil, errors.New("dependent change has no recorded version")
		}
		if latest.ID != dependent.ID || !slices.Contains(latest.Dependencies, fromVersion) {
			continue
		}
		if _, conflicted := conflicts[dependentReconciliationConflictKey{
			ReplacedDependency: fromVersion,
			AcceptedVersion:    toVersion,
			DependentVersion:   latest.ID,
			BaseIntent:         currentIntent,
		}]; conflicted {
			continue
		}
		seenChanges[dependent.ChangeID] = struct{}{}
		if _, completed, err := repository.promotions.CompletedPromotion(ctx, latest.ID); err != nil {
			return nil, fmt.Errorf("read dependent promotion: %w", err)
		} else if completed {
			continue
		}
		change, found, err := repository.changes.Change(ctx, latest.ChangeID)
		if err != nil {
			return nil, fmt.Errorf("read dependent change: %w", err)
		}
		if !found {
			return nil, errors.New("dependent version change is not recorded")
		}
		candidates = append(candidates, DependentReconciliationCandidate{
			ReplacedDependency: fromVersion,
			AcceptedVersion:    toVersion,
			AcceptedAtIntent:   baseIntent,
			Dependent:          Proposed{Change: change, Version: latest},
		})
	}
	return candidates, nil
}

func (repository *Repository) dependentReconciliationConflicts(ctx context.Context) (map[dependentReconciliationConflictKey]struct{}, error) {
	const pageSize = 100
	conflicts := make(map[dependentReconciliationConflictKey]struct{})
	var after ConflictID
	for {
		page, more, err := repository.conflicts.ReconciliationConflicts(ctx, after, pageSize)
		if err != nil {
			return nil, fmt.Errorf("read dependent reconciliation conflicts: %w", err)
		}
		for _, conflict := range page {
			conflicts[dependentReconciliationConflictKey{
				ReplacedDependency: conflict.FromVersion,
				AcceptedVersion:    conflict.ToVersion,
				DependentVersion:   conflict.Version.ID,
				BaseIntent:         conflict.BaseIntent,
			}] = struct{}{}
		}
		if !more {
			return conflicts, nil
		}
		if len(page) == 0 {
			return nil, errors.New("reconciliation conflict history did not advance")
		}
		after = page[len(page)-1].ID
	}
}

// ReadyDependents returns unpromoted versions whose declared dependencies are
// complete and whose direct dependency produced the current accepted intent.
func (repository *Repository) ReadyDependents(ctx context.Context) ([]Proposed, error) {
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current intent: %w", err)
	}
	if !found {
		return nil, errors.New("intent ledger is not initialized")
	}
	currentPromotion, found, err := repository.promotions.CompletedPromotionByIntent(ctx, current.ID)
	if err != nil {
		return nil, fmt.Errorf("read promotion for current intent: %w", err)
	}
	if !found {
		return nil, nil
	}
	versions, err := repository.changes.Dependents(ctx, currentPromotion.Promotion.VersionID)
	if err != nil {
		return nil, fmt.Errorf("read current version dependents: %w", err)
	}
	ready := make([]Proposed, 0, len(versions))
	seenChanges := make(map[ChangeID]struct{}, len(versions))
	for _, version := range versions {
		if _, seen := seenChanges[version.ChangeID]; seen {
			continue
		}
		latest, found, err := repository.changes.LatestVersion(ctx, version.ChangeID)
		if err != nil {
			return nil, fmt.Errorf("read latest dependent version: %w", err)
		}
		if !found {
			return nil, errors.New("dependent change has no recorded version")
		}
		if latest.ID != version.ID {
			continue
		}
		seenChanges[version.ChangeID] = struct{}{}
		if _, promoted, err := repository.promotions.CompletedPromotion(ctx, version.ID); err != nil {
			return nil, fmt.Errorf("read dependent promotion: %w", err)
		} else if promoted {
			continue
		}
		dependenciesReady := true
		for _, dependencyID := range version.Dependencies {
			if _, promoted, err := repository.promotions.CompletedPromotion(ctx, dependencyID); err != nil {
				return nil, fmt.Errorf("read dependent dependency promotion: %w", err)
			} else if !promoted {
				dependenciesReady = false
				break
			}
		}
		if !dependenciesReady {
			continue
		}
		change, found, err := repository.changes.Change(ctx, version.ChangeID)
		if err != nil {
			return nil, fmt.Errorf("read dependent change: %w", err)
		}
		if !found {
			return nil, errors.New("dependent version change is not recorded")
		}
		ready = append(ready, Proposed{Change: change, Version: version})
	}
	return ready, nil
}
