package ledgerfs

import (
	"context"
	"errors"
	"slices"

	"github.com/sky-valley/grd/internal/intent"
)

func validateReconciliationResolution(state *journalState, record journalRecord) error {
	if state.current.ID == "" {
		return errors.New("reconciliation resolution precedes repository initialization")
	}
	if record.IdempotencyKey == "" || record.ReconciliationResolution == nil || record.Version == nil {
		return errors.New("invalid reconciliation resolution record")
	}
	resolution := *record.ReconciliationResolution
	version := *record.Version
	if existing, ok := state.idempotency[record.IdempotencyKey]; ok {
		if existing.operation != reconciliationResolutionOperation {
			return intent.ErrIdempotencyConflict
		}
		storedResolution, resolutionFound := state.resolutions[existing.conflictID]
		storedVersion, versionFound := state.versions[existing.versionID]
		if resolutionFound && versionFound && storedResolution == resolution && sameVersion(storedVersion, version) {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	conflict, found := state.conflicts[resolution.ConflictID]
	if !found {
		return intent.ErrReconciliationConflictNotFound
	}
	if _, resolved := state.resolutions[resolution.ConflictID]; resolved {
		return intent.ErrReconciliationConflictResolved
	}
	previous, found := state.versions[resolution.FromVersion]
	if !found || previous.ID != conflict.Version.ID || previous.ChangeID != conflict.Change.ID {
		return intent.ErrVersionNotFound
	}
	ids := state.versionIDs[previous.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != previous.ID {
		return intent.ErrVersionAdvanced
	}
	if state.pending != "" {
		return intent.ErrPromotionPending
	}
	targetPromotionID, promoted := state.completed[conflict.ToVersion]
	targetPromotion, found := state.promotions[targetPromotionID]
	if !promoted || !found || conflict.BaseIntent != state.current.ID ||
		!recordedRevisionDescendsFrom(state, state.current.ID, targetPromotion.ToIntent) ||
		resolution.BaseIntent != state.current.ID {
		return intent.ErrIntentAdvanced
	}
	if resolution.ID == "" || resolution.ToVersion != version.ID || resolution.ResolvedBy == "" || resolution.Rationale == "" {
		return errors.New("invalid reconciliation resolution identity")
	}
	if version.ID == "" || version.ChangeID != previous.ChangeID || version.BaseIntent != state.current.ID ||
		version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid reconciled version")
	}
	if !slices.Equal(version.Dependencies, reconciledDependencies(previous.Dependencies, conflict.FromVersion, conflict.ToVersion)) {
		return errors.New("reconciled version does not preserve unrelated dependencies")
	}
	if _, found := state.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}

func reconciledDependencies(dependencies []intent.VersionID, superseded ...intent.VersionID) []intent.VersionID {
	filtered := make([]intent.VersionID, 0, len(dependencies))
	for _, dependency := range dependencies {
		if slices.Contains(superseded, dependency) {
			continue
		}
		filtered = append(filtered, dependency)
	}
	return filtered
}

func applyReconciliationResolution(state *journalState, record journalRecord) {
	if _, exists := state.idempotency[record.IdempotencyKey]; exists {
		return
	}
	version := cloneVersion(*record.Version)
	resolution := *record.ReconciliationResolution
	state.versions[version.ID] = version
	state.versionIDs[version.ChangeID] = append(state.versionIDs[version.ChangeID], version.ID)
	for _, dependencyID := range version.Dependencies {
		state.dependents[dependencyID] = append(state.dependents[dependencyID], version.ID)
	}
	state.resolutions[resolution.ConflictID] = resolution
	state.idempotency[record.IdempotencyKey] = idempotencyRecord{
		operation:  reconciliationResolutionOperation,
		versionID:  version.ID,
		conflictID: resolution.ConflictID,
	}
	beginPendingEvaluation(state, version.ID, resolution.FromVersion)
	appendJournalHistory(state, intent.HistoryFact{Kind: intent.HistoryReconciliationResolved, Version: record.Version, ReconciliationResolution: record.ReconciliationResolution})
}

func (ledger *Ledger) ReconciliationResolution(ctx context.Context, conflictID intent.ConflictID) (intent.ReconciliationResolution, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.ReconciliationResolution{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.ReconciliationResolution{}, false, errors.New("journal is closed")
	}
	resolution, found := ledger.state.resolutions[conflictID]
	return resolution, found, nil
}

func (ledger *Ledger) ReconciliationResolutionByIdempotencyKey(ctx context.Context, key string) (intent.ResolvedReconciliationConflict, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.ResolvedReconciliationConflict{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.ResolvedReconciliationConflict{}, false, errors.New("journal is closed")
	}
	record, found := ledger.state.idempotency[key]
	if !found {
		return intent.ResolvedReconciliationConflict{}, false, nil
	}
	if record.operation != reconciliationResolutionOperation {
		return intent.ResolvedReconciliationConflict{}, false, intent.ErrIdempotencyConflict
	}
	resolution, resolved := ledger.state.resolutions[record.conflictID]
	version, versionFound := ledger.state.versions[record.versionID]
	conflict, conflictFound := ledger.state.conflicts[record.conflictID]
	if !resolved || !versionFound || !conflictFound {
		return intent.ResolvedReconciliationConflict{}, false, intent.ErrIdempotencyConflict
	}
	return intent.ResolvedReconciliationConflict{
		Change:     conflict.Change,
		Version:    cloneVersion(version),
		Resolution: resolution,
	}, true, nil
}

func (ledger *Ledger) RecordReconciliationResolution(ctx context.Context, key string, resolution intent.ReconciliationResolution, version intent.Version) error {
	copy := cloneVersion(version)
	return ledger.append(ctx, journalRecord{
		Format:                   journalFormat,
		Kind:                     reconciliationResolutionRecorded,
		IdempotencyKey:           key,
		Version:                  &copy,
		ReconciliationResolution: &resolution,
	})
}
