package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

var ErrReconciliationConflictResolved = errors.New("reconciliation conflict is already resolved")

type ResolutionID string

type ReconciliationResolution struct {
	ID          ResolutionID
	ConflictID  ConflictID
	FromVersion VersionID
	ToVersion   VersionID
	BaseIntent  RevisionID
	ResolvedBy  string
	Rationale   string
}

type ResolveReconciliationConflictRequest struct {
	IdempotencyKey  string
	ConflictID      ConflictID
	ExpectedVersion VersionID
	ExpectedIntent  RevisionID
	Content         ContentRef
	Producer        string
	ResolvedBy      string
	Rationale       string
}

type ResolvedReconciliationConflict struct {
	Change     Change
	Version    Version
	Resolution ReconciliationResolution
}

func (repository *Repository) ResolveReconciliationConflict(ctx context.Context, request ResolveReconciliationConflictRequest) (ResolvedReconciliationConflict, error) {
	if request.IdempotencyKey == "" {
		return ResolvedReconciliationConflict{}, errors.New("resolution idempotency key is required")
	}
	if request.ConflictID == "" || request.ExpectedVersion == "" || request.ExpectedIntent == "" {
		return ResolvedReconciliationConflict{}, errors.New("resolution conflict, version, and intent are required")
	}
	if request.Content.Engine == "" || request.Content.Revision == "" {
		return ResolvedReconciliationConflict{}, errors.New("resolved content reference requires engine and revision")
	}
	if request.Producer == "" || request.ResolvedBy == "" || request.Rationale == "" {
		return ResolvedReconciliationConflict{}, errors.New("resolution producer, actor, and rationale are required")
	}

	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()
	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()

	existing, found, err := repository.conflicts.ReconciliationResolutionByIdempotencyKey(ctx, request.IdempotencyKey)
	if err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("read reconciliation resolution idempotency record: %w", err)
	}
	if found {
		if existing.Resolution.ConflictID != request.ConflictID ||
			existing.Resolution.FromVersion != request.ExpectedVersion ||
			existing.Resolution.BaseIntent != request.ExpectedIntent ||
			existing.Version.Content != request.Content ||
			existing.Version.Producer != request.Producer ||
			existing.Resolution.ResolvedBy != request.ResolvedBy ||
			existing.Resolution.Rationale != request.Rationale {
			return ResolvedReconciliationConflict{}, ErrIdempotencyConflict
		}
		return existing, nil
	}

	inspection, found, err := repository.conflicts.ReconciliationConflict(ctx, request.ConflictID)
	if err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("read reconciliation conflict: %w", err)
	}
	if !found {
		return ResolvedReconciliationConflict{}, ErrReconciliationConflictNotFound
	}
	conflict := inspection.ReconciliationConflict
	if _, resolved, err := repository.conflicts.ReconciliationResolution(ctx, conflict.ID); err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("read existing reconciliation resolution: %w", err)
	} else if resolved {
		return ResolvedReconciliationConflict{}, ErrReconciliationConflictResolved
	}
	if conflict.Version.ID != request.ExpectedVersion {
		return ResolvedReconciliationConflict{}, ErrVersionNotFound
	}
	latest, found, err := repository.changes.LatestVersion(ctx, conflict.Change.ID)
	if err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("read latest reconciled version: %w", err)
	}
	if !found || latest.ID != conflict.Version.ID {
		return ResolvedReconciliationConflict{}, ErrVersionAdvanced
	}
	if _, found, err := repository.promotions.PendingPromotion(ctx); err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("read pending promotion before resolution: %w", err)
	} else if found {
		return ResolvedReconciliationConflict{}, ErrPromotionPending
	}
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("read current intent before resolution: %w", err)
	}
	if !found || current.ID != request.ExpectedIntent {
		return ResolvedReconciliationConflict{}, ErrIntentAdvanced
	}
	if conflict.BaseIntent != current.ID {
		return ResolvedReconciliationConflict{}, ErrIntentAdvanced
	}
	targetPromotion, found, err := repository.promotions.CompletedPromotion(ctx, conflict.ToVersion)
	if err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("read reconciliation target promotion: %w", err)
	}
	if !found {
		return ResolvedReconciliationConflict{}, ErrIntentAdvanced
	}
	acceptedInHistory, err := repository.intentDescendsFrom(ctx, current, targetPromotion.Intent.ID)
	if err != nil {
		return ResolvedReconciliationConflict{}, err
	}
	if !acceptedInHistory {
		return ResolvedReconciliationConflict{}, ErrIntentAdvanced
	}

	versionID, err := newID("version")
	if err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("create resolved version id: %w", err)
	}
	resolutionID, err := newID("resolution")
	if err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("create reconciliation resolution id: %w", err)
	}
	version := Version{
		ID:           VersionID(versionID),
		ChangeID:     conflict.Change.ID,
		BaseIntent:   current.ID,
		Content:      request.Content,
		Producer:     request.Producer,
		Dependencies: resolutionDependencies(conflict.Version.Dependencies, conflict.FromVersion, conflict.ToVersion),
	}
	if err := repository.admission.Admit(ctx, version.ID, version.Content); err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("admit resolved content: %w", err)
	}
	resolution := ReconciliationResolution{
		ID:          ResolutionID(resolutionID),
		ConflictID:  conflict.ID,
		FromVersion: conflict.Version.ID,
		ToVersion:   version.ID,
		BaseIntent:  current.ID,
		ResolvedBy:  request.ResolvedBy,
		Rationale:   request.Rationale,
	}
	if err := repository.conflicts.RecordReconciliationResolution(ctx, request.IdempotencyKey, resolution, version); err != nil {
		return ResolvedReconciliationConflict{}, fmt.Errorf("record reconciliation resolution: %w", err)
	}
	return ResolvedReconciliationConflict{
		Change:     conflict.Change,
		Version:    cloneVersion(version),
		Resolution: resolution,
	}, nil
}

func resolutionDependencies(dependencies []VersionID, superseded ...VersionID) []VersionID {
	filtered := make([]VersionID, 0, len(dependencies))
	for _, dependency := range dependencies {
		if slices.Contains(superseded, dependency) {
			continue
		}
		filtered = append(filtered, dependency)
	}
	return filtered
}
