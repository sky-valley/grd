package intent

import (
	"context"
	"errors"
	"slices"
)

func (ledger *transientLedger) ReconciliationResolution(_ context.Context, conflictID ConflictID) (ReconciliationResolution, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	resolution, found := ledger.resolutions[conflictID]
	return resolution, found, nil
}

func (ledger *transientLedger) ReconciliationResolutionByIdempotencyKey(_ context.Context, key string) (ResolvedReconciliationConflict, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return ResolvedReconciliationConflict{}, false, nil
	}
	if record.operation != transientReconciliationResolutionOperation {
		return ResolvedReconciliationConflict{}, false, ErrIdempotencyConflict
	}
	resolution, resolved := ledger.resolutions[record.conflictID]
	version, versionFound := ledger.versions[record.versionID]
	conflict, conflictFound := ledger.conflicts[record.conflictID]
	if !resolved || !versionFound || !conflictFound {
		return ResolvedReconciliationConflict{}, false, ErrIdempotencyConflict
	}
	return ResolvedReconciliationConflict{
		Change:     conflict.Change,
		Version:    cloneVersion(version),
		Resolution: resolution,
	}, true, nil
}

func (ledger *transientLedger) RecordReconciliationResolution(_ context.Context, key string, resolution ReconciliationResolution, version Version) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		storedResolution, resolutionFound := ledger.resolutions[existing.conflictID]
		storedVersion, versionFound := ledger.versions[existing.versionID]
		if existing.operation == transientReconciliationResolutionOperation &&
			resolutionFound && versionFound &&
			storedResolution == resolution && versionsEqual(storedVersion, version) {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if err := validateReconciliationResolutionRecord(ledger, resolution, version); err != nil {
		return err
	}
	ledger.versions[version.ID] = cloneVersion(version)
	ledger.versionIDs[version.ChangeID] = append(ledger.versionIDs[version.ChangeID], version.ID)
	for _, dependencyID := range version.Dependencies {
		ledger.dependents[dependencyID] = append(ledger.dependents[dependencyID], version.ID)
	}
	ledger.resolutions[resolution.ConflictID] = resolution
	ledger.idempotency[key] = transientIdempotencyRecord{
		operation:  transientReconciliationResolutionOperation,
		versionID:  version.ID,
		conflictID: resolution.ConflictID,
	}
	ledger.beginPendingEvaluation(version.ID, resolution.FromVersion)
	return nil
}

func validateReconciliationResolutionRecord(ledger *transientLedger, resolution ReconciliationResolution, version Version) error {
	conflict, found := ledger.conflicts[resolution.ConflictID]
	if !found {
		return ErrReconciliationConflictNotFound
	}
	if _, resolved := ledger.resolutions[resolution.ConflictID]; resolved {
		return ErrReconciliationConflictResolved
	}
	previous, found := ledger.versions[resolution.FromVersion]
	if !found || previous.ID != conflict.Version.ID || previous.ChangeID != conflict.Change.ID {
		return ErrVersionNotFound
	}
	ids := ledger.versionIDs[previous.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != previous.ID {
		return ErrVersionAdvanced
	}
	if ledger.pending != "" {
		return ErrPromotionPending
	}
	targetPromotionID, promoted := ledger.completed[conflict.ToVersion]
	targetPromotion, found := ledger.promotions[targetPromotionID]
	if !promoted || !found || conflict.BaseIntent != ledger.current.ID ||
		!transientRevisionDescendsFrom(ledger, ledger.current.ID, targetPromotion.ToIntent) {
		return ErrIntentAdvanced
	}
	if resolution.ID == "" || resolution.ToVersion != version.ID ||
		resolution.BaseIntent == "" || resolution.ResolvedBy == "" || resolution.Rationale == "" {
		return errors.New("invalid reconciliation resolution identity")
	}
	if resolution.BaseIntent != ledger.current.ID {
		return ErrIntentAdvanced
	}
	if version.ID == "" || version.ChangeID != previous.ChangeID || version.BaseIntent != ledger.current.ID ||
		version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid reconciled version")
	}
	if !slices.Equal(version.Dependencies, resolutionDependencies(previous.Dependencies, conflict.FromVersion, conflict.ToVersion)) {
		return errors.New("reconciled version does not preserve unrelated dependencies")
	}
	if _, found := ledger.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}
