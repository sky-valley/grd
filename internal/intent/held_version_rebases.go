package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

type HeldVersionRebase struct {
	FromVersion VersionID
	ToVersion   VersionID
	FromIntent  RevisionID
	ToIntent    RevisionID
	Rationale   string
}

type RebaseHeldVersionRequest struct {
	IdempotencyKey  string
	ExpectedVersion VersionID
	ExpectedIntent  RevisionID
	Content         ContentRef
	Producer        string
	Rationale       string
}

type RebasedHeldVersion struct {
	Change  Change
	Version Version
	Rebase  HeldVersionRebase
}

type HeldVersionRebaseCandidate struct {
	Held Proposed
}

func (repository *Repository) RebaseHeldVersion(ctx context.Context, request RebaseHeldVersionRequest) (RebasedHeldVersion, error) {
	if request.IdempotencyKey == "" {
		return RebasedHeldVersion{}, errors.New("held version rebase idempotency key is required")
	}
	if request.ExpectedVersion == "" || request.ExpectedIntent == "" {
		return RebasedHeldVersion{}, errors.New("held version rebase version and intent are required")
	}
	if request.Content.Engine == "" || request.Content.Revision == "" {
		return RebasedHeldVersion{}, errors.New("rebased content reference requires engine and revision")
	}
	if request.Producer == "" || request.Rationale == "" {
		return RebasedHeldVersion{}, errors.New("held version rebase producer and rationale are required")
	}

	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()
	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()

	existing, found, err := repository.rebases.HeldVersionRebaseByIdempotencyKey(ctx, request.IdempotencyKey)
	if err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("read held version rebase idempotency record: %w", err)
	}
	if found {
		if existing.Rebase.FromVersion != request.ExpectedVersion ||
			existing.Rebase.ToIntent != request.ExpectedIntent ||
			existing.Version.Content != request.Content ||
			existing.Version.Producer != request.Producer ||
			existing.Rebase.Rationale != request.Rationale {
			return RebasedHeldVersion{}, ErrIdempotencyConflict
		}
		return existing, nil
	}

	if _, found, err := repository.promotions.PendingPromotion(ctx); err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("read pending promotion before held version rebase: %w", err)
	} else if found {
		return RebasedHeldVersion{}, ErrPromotionPending
	}
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("read current intent before held version rebase: %w", err)
	}
	if !found || current.ID != request.ExpectedIntent {
		return RebasedHeldVersion{}, ErrIntentAdvanced
	}
	previous, found, err := repository.changes.Version(ctx, request.ExpectedVersion)
	if err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("read held version: %w", err)
	}
	if !found {
		return RebasedHeldVersion{}, ErrVersionNotFound
	}
	latest, found, err := repository.changes.LatestVersion(ctx, previous.ChangeID)
	if err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("read latest held version: %w", err)
	}
	if !found || latest.ID != previous.ID {
		return RebasedHeldVersion{}, ErrVersionAdvanced
	}
	if previous.BaseIntent == current.ID {
		return RebasedHeldVersion{}, errors.New("held version is already based on current intent")
	}
	basedOnHistory, err := repository.intentDescendsFrom(ctx, current, previous.BaseIntent)
	if err != nil {
		return RebasedHeldVersion{}, err
	}
	if !basedOnHistory {
		return RebasedHeldVersion{}, ErrIntentAdvanced
	}
	if _, promoted, err := repository.promotions.CompletedPromotion(ctx, previous.ID); err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("read held version promotion: %w", err)
	} else if promoted {
		return RebasedHeldVersion{}, ErrVersionPromotionStarted
	}
	change, found, err := repository.changes.Change(ctx, previous.ChangeID)
	if err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("read held change: %w", err)
	}
	if !found {
		return RebasedHeldVersion{}, ErrChangeNotFound
	}

	versionID, err := newID("version")
	if err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("create rebased held version id: %w", err)
	}
	version := Version{
		ID:           VersionID(versionID),
		ChangeID:     change.ID,
		BaseIntent:   current.ID,
		Content:      request.Content,
		Producer:     request.Producer,
		Dependencies: slices.Clone(previous.Dependencies),
	}
	if err := repository.admission.Admit(ctx, version.ID, version.Content); err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("admit rebased held content: %w", err)
	}
	rebase := HeldVersionRebase{
		FromVersion: previous.ID,
		ToVersion:   version.ID,
		FromIntent:  previous.BaseIntent,
		ToIntent:    current.ID,
		Rationale:   request.Rationale,
	}
	if err := repository.rebases.RecordHeldVersionRebase(ctx, request.IdempotencyKey, rebase, version); err != nil {
		return RebasedHeldVersion{}, fmt.Errorf("record held version rebase: %w", err)
	}
	return RebasedHeldVersion{Change: change, Version: cloneVersion(version), Rebase: rebase}, nil
}

// HeldVersionRebases derives stale, unpromoted latest versions whose Change
// entered reconciliation. The history is the discovery seed, not a work queue.
func (repository *Repository) HeldVersionRebases(ctx context.Context) ([]HeldVersionRebaseCandidate, error) {
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

	seenChanges := make(map[ChangeID]struct{})
	var changeIDs []ChangeID
	rememberChange := func(id ChangeID) {
		if _, seen := seenChanges[id]; seen {
			return
		}
		seenChanges[id] = struct{}{}
		changeIDs = append(changeIDs, id)
	}
	const pageSize = 100
	var after VersionID
	for {
		reconciliations, more, err := repository.reconciliations.DependentReconciliations(ctx, after, pageSize)
		if err != nil {
			return nil, fmt.Errorf("read dependent reconciliation history: %w", err)
		}
		for _, reconciliation := range reconciliations {
			version, found, err := repository.changes.Version(ctx, reconciliation.ToVersion)
			if err != nil {
				return nil, fmt.Errorf("read reconciled version: %w", err)
			}
			if !found {
				return nil, errors.New("dependent reconciliation version is not recorded")
			}
			rememberChange(version.ChangeID)
		}
		if !more {
			break
		}
		if len(reconciliations) == 0 {
			return nil, errors.New("dependent reconciliation history did not advance")
		}
		after = reconciliations[len(reconciliations)-1].ToVersion
	}

	var conflictAfter ConflictID
	for {
		conflicts, more, err := repository.conflicts.ReconciliationConflicts(ctx, conflictAfter, pageSize)
		if err != nil {
			return nil, fmt.Errorf("read reconciliation resolution history: %w", err)
		}
		for _, conflict := range conflicts {
			if conflict.Resolution != nil {
				rememberChange(conflict.Change.ID)
			}
		}
		if !more {
			break
		}
		if len(conflicts) == 0 {
			return nil, errors.New("reconciliation conflict history did not advance")
		}
		conflictAfter = conflicts[len(conflicts)-1].ID
	}

	candidates := make([]HeldVersionRebaseCandidate, 0, len(changeIDs))
	for _, changeID := range changeIDs {
		latest, found, err := repository.changes.LatestVersion(ctx, changeID)
		if err != nil {
			return nil, fmt.Errorf("read latest reconciled change version: %w", err)
		}
		if !found || latest.BaseIntent == current.ID {
			continue
		}
		if _, promoted, err := repository.promotions.CompletedPromotion(ctx, latest.ID); err != nil {
			return nil, fmt.Errorf("read reconciled change promotion: %w", err)
		} else if promoted {
			continue
		}
		basedOnHistory, err := repository.intentDescendsFrom(ctx, current, latest.BaseIntent)
		if err != nil {
			return nil, err
		}
		if !basedOnHistory {
			continue
		}
		change, found, err := repository.changes.Change(ctx, changeID)
		if err != nil {
			return nil, fmt.Errorf("read reconciled change: %w", err)
		}
		if !found {
			return nil, errors.New("reconciled version change is not recorded")
		}
		candidates = append(candidates, HeldVersionRebaseCandidate{
			Held: Proposed{Change: change, Version: latest},
		})
	}
	return candidates, nil
}
