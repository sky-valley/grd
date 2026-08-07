package ledgerfs

import (
	"context"
	"errors"
	"slices"

	"github.com/sky-valley/grd/internal/intent"
)

func validateDependentReconciliation(state *journalState, record journalRecord) error {
	if state.current.ID == "" {
		return errors.New("dependent reconciliation precedes repository initialization")
	}
	if record.IdempotencyKey == "" || record.DependentReconciliation == nil || record.Version == nil {
		return errors.New("invalid dependent reconciliation record")
	}
	reconciliation := *record.DependentReconciliation
	version := *record.Version
	if existing, ok := state.idempotency[record.IdempotencyKey]; ok {
		if existing.operation != dependentReconciliationOperation {
			return intent.ErrIdempotencyConflict
		}
		storedReconciliation, reconciliationFound := state.reconciliations[existing.versionID]
		storedVersion, versionFound := state.versions[existing.versionID]
		if reconciliationFound && versionFound &&
			storedReconciliation == reconciliation && sameVersion(storedVersion, version) {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	previous, found := state.versions[reconciliation.FromVersion]
	if !found {
		return intent.ErrVersionNotFound
	}
	ids := state.versionIDs[previous.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != previous.ID {
		return intent.ErrVersionAdvanced
	}
	if _, promoted := state.completed[previous.ID]; promoted {
		return intent.ErrVersionPromotionStarted
	}
	if state.pending != "" {
		return intent.ErrPromotionPending
	}
	acceptedPromotionID, promoted := state.completed[reconciliation.AcceptedVersion]
	acceptedPromotion, found := state.promotions[acceptedPromotionID]
	if !promoted || !found || !recordedRevisionDescendsFrom(state, state.current.ID, acceptedPromotion.ToIntent) ||
		reconciliation.BaseIntent != state.current.ID {
		return intent.ErrIntentAdvanced
	}
	if !recordedAmendmentDescendant(state, reconciliation.ReplacedDependency, reconciliation.AcceptedVersion) {
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
	replaced, found := state.versions[reconciliation.ReplacedDependency]
	if !found {
		return intent.ErrVersionNotFound
	}
	if replaced.ChangeID == previous.ChangeID {
		return errors.New("dependent reconciliation cannot replace a version of the same change")
	}
	if version.ID == "" || version.ChangeID != previous.ChangeID || version.BaseIntent != state.current.ID ||
		version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid reconciled dependent version")
	}
	if !slices.Equal(version.Dependencies, reconciledDependencies(
		previous.Dependencies,
		reconciliation.ReplacedDependency,
		reconciliation.AcceptedVersion,
	)) {
		return errors.New("reconciled dependent does not preserve unrelated dependencies")
	}
	if _, found := state.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}

func recordedAmendmentDescendant(state *journalState, ancestor, descendant intent.VersionID) bool {
	for current := descendant; current != ""; {
		amendment, found := state.amendments[current]
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

func recordedRevisionDescendsFrom(state *journalState, revisionID, ancestor intent.RevisionID) bool {
	for revisionID != "" {
		if revisionID == ancestor {
			return true
		}
		revision, found := state.revisions[revisionID]
		if !found {
			return false
		}
		revisionID = revision.PreviousID
	}
	return false
}

func applyDependentReconciliation(state *journalState, record journalRecord) {
	if _, exists := state.idempotency[record.IdempotencyKey]; exists {
		return
	}
	version := cloneVersion(*record.Version)
	reconciliation := *record.DependentReconciliation
	state.versions[version.ID] = version
	state.versionIDs[version.ChangeID] = append(state.versionIDs[version.ChangeID], version.ID)
	for _, dependencyID := range version.Dependencies {
		state.dependents[dependencyID] = append(state.dependents[dependencyID], version.ID)
	}
	state.reconciliations[version.ID] = reconciliation
	state.reconciliationIDs = append(state.reconciliationIDs, version.ID)
	state.idempotency[record.IdempotencyKey] = idempotencyRecord{
		operation: dependentReconciliationOperation,
		versionID: version.ID,
	}
	beginPendingEvaluation(state, version.ID, reconciliation.FromVersion)
}

func (ledger *Ledger) DependentReconciliations(ctx context.Context, after intent.VersionID, limit int) ([]intent.DependentReconciliation, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	start := 0
	if after != "" {
		start = slices.Index(ledger.state.reconciliationIDs, after)
		if start < 0 {
			return nil, false, errors.New("dependent reconciliation cursor not found")
		}
		start++
	}
	end := min(start+limit, len(ledger.state.reconciliationIDs))
	reconciliations := make([]intent.DependentReconciliation, 0, end-start)
	for _, id := range ledger.state.reconciliationIDs[start:end] {
		reconciliations = append(reconciliations, ledger.state.reconciliations[id])
	}
	return reconciliations, end < len(ledger.state.reconciliationIDs), nil
}

func (ledger *Ledger) DependentReconciliation(ctx context.Context, toVersion intent.VersionID) (intent.DependentReconciliation, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.DependentReconciliation{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.DependentReconciliation{}, false, errors.New("journal is closed")
	}
	reconciliation, found := ledger.state.reconciliations[toVersion]
	return reconciliation, found, nil
}

func (ledger *Ledger) DependentReconciliationByIdempotencyKey(ctx context.Context, key string) (intent.ReconciledDependent, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.ReconciledDependent{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.ReconciledDependent{}, false, errors.New("journal is closed")
	}
	record, found := ledger.state.idempotency[key]
	if !found {
		return intent.ReconciledDependent{}, false, nil
	}
	if record.operation != dependentReconciliationOperation {
		return intent.ReconciledDependent{}, false, intent.ErrIdempotencyConflict
	}
	reconciliation, reconciled := ledger.state.reconciliations[record.versionID]
	version, versionFound := ledger.state.versions[record.versionID]
	change, changeFound := ledger.state.changes[version.ChangeID]
	if !reconciled || !versionFound || !changeFound {
		return intent.ReconciledDependent{}, false, intent.ErrIdempotencyConflict
	}
	return intent.ReconciledDependent{
		Change:         change,
		Version:        cloneVersion(version),
		Reconciliation: reconciliation,
	}, true, nil
}

func (ledger *Ledger) RecordDependentReconciliation(
	ctx context.Context,
	key string,
	reconciliation intent.DependentReconciliation,
	version intent.Version,
) error {
	copy := cloneVersion(version)
	return ledger.append(ctx, journalRecord{
		Format:                  journalFormat,
		Kind:                    dependentReconciliationRecorded,
		IdempotencyKey:          key,
		Version:                 &copy,
		DependentReconciliation: &reconciliation,
	})
}
