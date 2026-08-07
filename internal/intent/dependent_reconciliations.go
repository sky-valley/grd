package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

type DependentReconciliation struct {
	FromVersion        VersionID
	ToVersion          VersionID
	ReplacedDependency VersionID
	AcceptedVersion    VersionID
	BaseIntent         RevisionID
	Rationale          string
}

type ReconcileDependentRequest struct {
	IdempotencyKey     string
	ExpectedVersion    VersionID
	ReplacedDependency VersionID
	AcceptedVersion    VersionID
	ExpectedIntent     RevisionID
	Content            ContentRef
	Producer           string
	Rationale          string
}

type ReconciledDependent struct {
	Change         Change
	Version        Version
	Reconciliation DependentReconciliation
}

func (repository *Repository) ReconcileDependent(ctx context.Context, request ReconcileDependentRequest) (ReconciledDependent, error) {
	if request.IdempotencyKey == "" {
		return ReconciledDependent{}, errors.New("dependent reconciliation idempotency key is required")
	}
	if request.ExpectedVersion == "" || request.ReplacedDependency == "" ||
		request.AcceptedVersion == "" || request.ExpectedIntent == "" {
		return ReconciledDependent{}, errors.New("dependent reconciliation versions and intent are required")
	}
	if request.ReplacedDependency == request.AcceptedVersion {
		return ReconciledDependent{}, errors.New("dependent reconciliation requires a replacement version")
	}
	if request.Content.Engine == "" || request.Content.Revision == "" {
		return ReconciledDependent{}, errors.New("reconciled content reference requires engine and revision")
	}
	if request.Producer == "" || request.Rationale == "" {
		return ReconciledDependent{}, errors.New("dependent reconciliation producer and rationale are required")
	}

	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()
	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()

	existing, found, err := repository.reconciliations.DependentReconciliationByIdempotencyKey(ctx, request.IdempotencyKey)
	if err != nil {
		return ReconciledDependent{}, fmt.Errorf("read dependent reconciliation idempotency record: %w", err)
	}
	if found {
		if existing.Reconciliation.FromVersion != request.ExpectedVersion ||
			existing.Reconciliation.ReplacedDependency != request.ReplacedDependency ||
			existing.Reconciliation.AcceptedVersion != request.AcceptedVersion ||
			existing.Reconciliation.BaseIntent != request.ExpectedIntent ||
			existing.Version.Content != request.Content ||
			existing.Version.Producer != request.Producer ||
			existing.Reconciliation.Rationale != request.Rationale {
			return ReconciledDependent{}, ErrIdempotencyConflict
		}
		return existing, nil
	}

	if _, found, err := repository.promotions.PendingPromotion(ctx); err != nil {
		return ReconciledDependent{}, fmt.Errorf("read pending promotion before dependent reconciliation: %w", err)
	} else if found {
		return ReconciledDependent{}, ErrPromotionPending
	}
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return ReconciledDependent{}, fmt.Errorf("read current intent before dependent reconciliation: %w", err)
	}
	if !found || current.ID != request.ExpectedIntent {
		return ReconciledDependent{}, ErrIntentAdvanced
	}
	acceptedPromotion, found, err := repository.promotions.CompletedPromotion(ctx, request.AcceptedVersion)
	if err != nil {
		return ReconciledDependent{}, fmt.Errorf("read accepted replacement promotion: %w", err)
	}
	if !found {
		return ReconciledDependent{}, ErrIntentAdvanced
	}
	acceptedInHistory, err := repository.intentDescendsFrom(ctx, current, acceptedPromotion.Intent.ID)
	if err != nil {
		return ReconciledDependent{}, err
	}
	if !acceptedInHistory {
		return ReconciledDependent{}, ErrIntentAdvanced
	}
	if related, err := repository.isAmendmentDescendant(ctx, request.ReplacedDependency, request.AcceptedVersion); err != nil {
		return ReconciledDependent{}, err
	} else if !related {
		return ReconciledDependent{}, errors.New("accepted version does not replace the declared dependency")
	}

	previous, found, err := repository.changes.Version(ctx, request.ExpectedVersion)
	if err != nil {
		return ReconciledDependent{}, fmt.Errorf("read dependent version: %w", err)
	}
	if !found {
		return ReconciledDependent{}, ErrVersionNotFound
	}
	latest, found, err := repository.changes.LatestVersion(ctx, previous.ChangeID)
	if err != nil {
		return ReconciledDependent{}, fmt.Errorf("read latest dependent version: %w", err)
	}
	if !found || latest.ID != previous.ID {
		return ReconciledDependent{}, ErrVersionAdvanced
	}
	if !slices.Contains(previous.Dependencies, request.ReplacedDependency) {
		return ReconciledDependent{}, errors.New("dependent version does not reference the replaced dependency")
	}
	if _, promoted, err := repository.promotions.CompletedPromotion(ctx, previous.ID); err != nil {
		return ReconciledDependent{}, fmt.Errorf("read dependent promotion: %w", err)
	} else if promoted {
		return ReconciledDependent{}, ErrVersionPromotionStarted
	}
	replaced, found, err := repository.changes.Version(ctx, request.ReplacedDependency)
	if err != nil {
		return ReconciledDependent{}, fmt.Errorf("read replaced dependency: %w", err)
	}
	if !found {
		return ReconciledDependent{}, ErrVersionNotFound
	}
	if replaced.ChangeID == previous.ChangeID {
		return ReconciledDependent{}, errors.New("dependent reconciliation cannot replace a version of the same change")
	}
	change, found, err := repository.changes.Change(ctx, previous.ChangeID)
	if err != nil {
		return ReconciledDependent{}, fmt.Errorf("read dependent change: %w", err)
	}
	if !found {
		return ReconciledDependent{}, ErrChangeNotFound
	}

	versionID, err := newID("version")
	if err != nil {
		return ReconciledDependent{}, fmt.Errorf("create reconciled dependent version id: %w", err)
	}
	version := Version{
		ID:           VersionID(versionID),
		ChangeID:     change.ID,
		BaseIntent:   current.ID,
		Content:      request.Content,
		Producer:     request.Producer,
		Dependencies: resolutionDependencies(previous.Dependencies, request.ReplacedDependency, request.AcceptedVersion),
	}
	if err := repository.admission.Admit(ctx, version.ID, version.Content); err != nil {
		return ReconciledDependent{}, fmt.Errorf("admit reconciled dependent content: %w", err)
	}
	reconciliation := DependentReconciliation{
		FromVersion:        previous.ID,
		ToVersion:          version.ID,
		ReplacedDependency: request.ReplacedDependency,
		AcceptedVersion:    request.AcceptedVersion,
		BaseIntent:         current.ID,
		Rationale:          request.Rationale,
	}
	if err := repository.reconciliations.RecordDependentReconciliation(ctx, request.IdempotencyKey, reconciliation, version); err != nil {
		return ReconciledDependent{}, fmt.Errorf("record dependent reconciliation: %w", err)
	}
	return ReconciledDependent{
		Change:         change,
		Version:        cloneVersion(version),
		Reconciliation: reconciliation,
	}, nil
}

func (repository *Repository) isAmendmentDescendant(ctx context.Context, ancestor, descendant VersionID) (bool, error) {
	for current := descendant; current != ""; {
		amendment, found, err := repository.amendments.Amendment(ctx, current)
		if err != nil {
			return false, fmt.Errorf("read replacement amendment history: %w", err)
		}
		if !found {
			return false, nil
		}
		if amendment.FromVersion == ancestor {
			return true, nil
		}
		current = amendment.FromVersion
	}
	return false, nil
}

func (repository *Repository) intentDescendsFrom(ctx context.Context, revision Revision, ancestor RevisionID) (bool, error) {
	for {
		if revision.ID == ancestor {
			return true, nil
		}
		if revision.PreviousID == "" {
			return false, nil
		}
		var found bool
		var err error
		revision, found, err = repository.intents.Revision(ctx, revision.PreviousID)
		if err != nil {
			return false, fmt.Errorf("read accepted intent history: %w", err)
		}
		if !found {
			return false, errors.New("accepted intent history is incomplete")
		}
	}
}
