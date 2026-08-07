package intent

import (
	"context"
	"errors"
	"slices"
)

func (ledger *transientLedger) DependentReconciliation(_ context.Context, toVersion VersionID) (DependentReconciliation, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	reconciliation, found := ledger.reconciliations[toVersion]
	return reconciliation, found, nil
}

func (ledger *transientLedger) DependentReconciliations(_ context.Context, after VersionID, limit int) ([]DependentReconciliation, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	start := 0
	if after != "" {
		start = slices.Index(ledger.reconciliationIDs, after)
		if start < 0 {
			return nil, false, errors.New("dependent reconciliation cursor not found")
		}
		start++
	}
	end := min(start+limit, len(ledger.reconciliationIDs))
	reconciliations := make([]DependentReconciliation, 0, end-start)
	for _, id := range ledger.reconciliationIDs[start:end] {
		reconciliations = append(reconciliations, ledger.reconciliations[id])
	}
	return reconciliations, end < len(ledger.reconciliationIDs), nil
}

func (ledger *transientLedger) DependentReconciliationByIdempotencyKey(_ context.Context, key string) (ReconciledDependent, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return ReconciledDependent{}, false, nil
	}
	if record.operation != transientDependentReconciliationOperation {
		return ReconciledDependent{}, false, ErrIdempotencyConflict
	}
	reconciliation, reconciled := ledger.reconciliations[record.versionID]
	version, versionFound := ledger.versions[record.versionID]
	change, changeFound := ledger.changes[version.ChangeID]
	if !reconciled || !versionFound || !changeFound {
		return ReconciledDependent{}, false, ErrIdempotencyConflict
	}
	return ReconciledDependent{
		Change:         change,
		Version:        cloneVersion(version),
		Reconciliation: reconciliation,
	}, true, nil
}

func (ledger *transientLedger) RecordDependentReconciliation(_ context.Context, key string, reconciliation DependentReconciliation, version Version) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		storedReconciliation, reconciliationFound := ledger.reconciliations[existing.versionID]
		storedVersion, versionFound := ledger.versions[existing.versionID]
		if existing.operation == transientDependentReconciliationOperation &&
			reconciliationFound && versionFound &&
			storedReconciliation == reconciliation && versionsEqual(storedVersion, version) {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if err := validateDependentReconciliationRecord(ledger, reconciliation, version); err != nil {
		return err
	}
	ledger.versions[version.ID] = cloneVersion(version)
	ledger.versionIDs[version.ChangeID] = append(ledger.versionIDs[version.ChangeID], version.ID)
	for _, dependencyID := range version.Dependencies {
		ledger.dependents[dependencyID] = append(ledger.dependents[dependencyID], version.ID)
	}
	ledger.reconciliations[version.ID] = reconciliation
	ledger.reconciliationIDs = append(ledger.reconciliationIDs, version.ID)
	ledger.idempotency[key] = transientIdempotencyRecord{
		operation: transientDependentReconciliationOperation,
		versionID: version.ID,
	}
	ledger.beginPendingEvaluation(version.ID, reconciliation.FromVersion)
	return nil
}

func validateDependentReconciliationRecord(ledger *transientLedger, reconciliation DependentReconciliation, version Version) error {
	previous, found := ledger.versions[reconciliation.FromVersion]
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
	acceptedPromotionID, promoted := ledger.completed[reconciliation.AcceptedVersion]
	acceptedPromotion, found := ledger.promotions[acceptedPromotionID]
	if !promoted || !found || !transientRevisionDescendsFrom(ledger, ledger.current.ID, acceptedPromotion.ToIntent) ||
		reconciliation.BaseIntent != ledger.current.ID {
		return ErrIntentAdvanced
	}
	if !transientAmendmentDescendant(ledger, reconciliation.ReplacedDependency, reconciliation.AcceptedVersion) {
		return errors.New("accepted version does not replace the declared dependency")
	}
	if reconciliation.ToVersion == "" || reconciliation.ToVersion != version.ID ||
		reconciliation.ReplacedDependency == "" || reconciliation.AcceptedVersion == "" ||
		reconciliation.Rationale == "" {
		return errors.New("invalid dependent reconciliation identity")
	}
	if !slices.Contains(previous.Dependencies, reconciliation.ReplacedDependency) {
		return errors.New("dependent version does not reference the replaced dependency")
	}
	replaced, found := ledger.versions[reconciliation.ReplacedDependency]
	if !found {
		return ErrVersionNotFound
	}
	if replaced.ChangeID == previous.ChangeID {
		return errors.New("dependent reconciliation cannot replace a version of the same change")
	}
	if version.ID == "" || version.ChangeID != previous.ChangeID || version.BaseIntent != ledger.current.ID ||
		version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid reconciled dependent version")
	}
	if !slices.Equal(version.Dependencies, resolutionDependencies(previous.Dependencies, reconciliation.ReplacedDependency, reconciliation.AcceptedVersion)) {
		return errors.New("reconciled dependent does not preserve unrelated dependencies")
	}
	if _, found := ledger.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}

func transientRevisionDescendsFrom(ledger *transientLedger, revisionID, ancestor RevisionID) bool {
	for revisionID != "" {
		if revisionID == ancestor {
			return true
		}
		revision, found := ledger.revisions[revisionID]
		if !found {
			return false
		}
		revisionID = revision.PreviousID
	}
	return false
}

func transientAmendmentDescendant(ledger *transientLedger, ancestor, descendant VersionID) bool {
	for current := descendant; current != ""; {
		amendment, found := ledger.amendments[current]
		if !found {
			return false
		}
		if amendment.FromVersion == ancestor {
			return true
		}
		current = amendment.FromVersion
	}
	return false
}
