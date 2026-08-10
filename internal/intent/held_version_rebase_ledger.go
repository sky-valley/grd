package intent

import (
	"context"
	"errors"
	"slices"
)

func (ledger *transientLedger) HeldVersionRebase(_ context.Context, toVersion VersionID) (HeldVersionRebase, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	rebase, found := ledger.rebases[toVersion]
	return rebase, found, nil
}

func (ledger *transientLedger) HeldVersionRebaseByIdempotencyKey(_ context.Context, key string) (RebasedHeldVersion, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return RebasedHeldVersion{}, false, nil
	}
	if record.operation != transientHeldVersionRebaseOperation {
		return RebasedHeldVersion{}, false, ErrIdempotencyConflict
	}
	rebase, rebased := ledger.rebases[record.versionID]
	version, versionFound := ledger.versions[record.versionID]
	change, changeFound := ledger.changes[version.ChangeID]
	if !rebased || !versionFound || !changeFound {
		return RebasedHeldVersion{}, false, ErrIdempotencyConflict
	}
	return RebasedHeldVersion{Change: change, Version: cloneVersion(version), Rebase: rebase}, true, nil
}

func (ledger *transientLedger) RecordHeldVersionRebase(_ context.Context, key string, rebase HeldVersionRebase, version Version) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		storedRebase, rebaseFound := ledger.rebases[existing.versionID]
		storedVersion, versionFound := ledger.versions[existing.versionID]
		if existing.operation == transientHeldVersionRebaseOperation &&
			rebaseFound && versionFound &&
			storedRebase == rebase && versionsEqual(storedVersion, version) {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if err := validateHeldVersionRebaseRecord(ledger, rebase, version); err != nil {
		return err
	}
	ledger.versions[version.ID] = cloneVersion(version)
	ledger.versionIDs[version.ChangeID] = append(ledger.versionIDs[version.ChangeID], version.ID)
	for _, dependencyID := range version.Dependencies {
		ledger.dependents[dependencyID] = append(ledger.dependents[dependencyID], version.ID)
	}
	ledger.rebases[version.ID] = rebase
	ledger.idempotency[key] = transientIdempotencyRecord{
		operation: transientHeldVersionRebaseOperation,
		versionID: version.ID,
	}
	ledger.beginPendingEvaluation(version.ID, rebase.FromVersion)
	appendHistoryFact(&ledger.history, HistoryFact{Kind: HistoryHeldVersionRebased, Version: &version, HeldVersionRebase: &rebase})
	return nil
}

func validateHeldVersionRebaseRecord(ledger *transientLedger, rebase HeldVersionRebase, version Version) error {
	previous, found := ledger.versions[rebase.FromVersion]
	if !found {
		return ErrVersionNotFound
	}
	ids := ledger.versionIDs[previous.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != previous.ID {
		return ErrVersionAdvanced
	}
	if _, promoted := ledger.completed[previous.ID]; promoted {
		return ErrVersionPromotionStarted
	}
	if ledger.pending != "" {
		return ErrPromotionPending
	}
	if rebase.ToVersion == "" || rebase.ToVersion != version.ID ||
		rebase.FromIntent != previous.BaseIntent ||
		rebase.ToIntent == "" || rebase.ToIntent != ledger.current.ID ||
		rebase.FromIntent == rebase.ToIntent || rebase.Rationale == "" {
		return errors.New("invalid held version rebase identity")
	}
	if !transientRevisionDescendsFrom(ledger, rebase.ToIntent, rebase.FromIntent) {
		return ErrIntentAdvanced
	}
	if version.ID == "" || version.ChangeID != previous.ChangeID || version.BaseIntent != rebase.ToIntent ||
		version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid rebased held version")
	}
	if !slices.Equal(version.Dependencies, previous.Dependencies) {
		return errors.New("rebased held version does not preserve dependencies")
	}
	if _, found := ledger.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}
