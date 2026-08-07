package intent

import (
	"context"
	"errors"
	"slices"
)

func (ledger *transientLedger) ReconciliationConflict(_ context.Context, id ConflictID) (ReconciliationConflictInspection, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	conflict, found := ledger.conflicts[id]
	inspection := ReconciliationConflictInspection{ReconciliationConflict: conflict}
	if resolution, resolved := ledger.resolutions[id]; resolved {
		inspection.Resolution = &resolution
	}
	return cloneReconciliationConflictInspection(inspection), found, nil
}

func (ledger *transientLedger) ReconciliationConflicts(_ context.Context, after ConflictID, limit int) ([]ReconciliationConflictInspection, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	start := 0
	if after != "" {
		start = -1
		for index, id := range ledger.conflictIDs {
			if id == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, ErrReconciliationConflictNotFound
		}
	}
	end := min(start+limit, len(ledger.conflictIDs))
	conflicts := make([]ReconciliationConflictInspection, 0, end-start)
	for _, id := range ledger.conflictIDs[start:end] {
		inspection := ReconciliationConflictInspection{ReconciliationConflict: ledger.conflicts[id]}
		if resolution, resolved := ledger.resolutions[id]; resolved {
			inspection.Resolution = &resolution
		}
		conflicts = append(conflicts, cloneReconciliationConflictInspection(inspection))
	}
	return conflicts, end < len(ledger.conflictIDs), nil
}

func (ledger *transientLedger) ReconciliationConflictByIdempotencyKey(_ context.Context, key string) (ReconciliationConflictInspection, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return ReconciliationConflictInspection{}, false, nil
	}
	if record.operation != transientReconciliationConflictOperation {
		return ReconciliationConflictInspection{}, false, ErrIdempotencyConflict
	}
	conflict, found := ledger.conflicts[record.conflictID]
	inspection := ReconciliationConflictInspection{ReconciliationConflict: conflict}
	if resolution, resolved := ledger.resolutions[record.conflictID]; resolved {
		inspection.Resolution = &resolution
	}
	return cloneReconciliationConflictInspection(inspection), found, nil
}

func (ledger *transientLedger) RecordReconciliationConflict(_ context.Context, key string, conflict ReconciliationConflict) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		stored, conflictFound := ledger.conflicts[existing.conflictID]
		if existing.operation == transientReconciliationConflictOperation &&
			conflictFound && reconciliationConflictsEqual(stored, conflict) {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if err := validateReconciliationConflictRecord(ledger, conflict); err != nil {
		return err
	}
	ledger.conflicts[conflict.ID] = cloneReconciliationConflict(conflict)
	ledger.conflictIDs = append(ledger.conflictIDs, conflict.ID)
	ledger.idempotency[key] = transientIdempotencyRecord{
		operation:  transientReconciliationConflictOperation,
		versionID:  conflict.Version.ID,
		conflictID: conflict.ID,
	}
	return nil
}

func validateReconciliationConflictRecord(ledger *transientLedger, conflict ReconciliationConflict) error {
	if conflict.ID == "" || conflict.Change.ID == "" || conflict.Version.ID == "" ||
		conflict.Version.ChangeID != conflict.Change.ID ||
		conflict.FromVersion == "" || conflict.ToVersion == "" || conflict.BaseIntent == "" || conflict.ReportedBy == "" {
		return errors.New("invalid reconciliation conflict identity")
	}
	from, fromFound := ledger.versions[conflict.FromVersion]
	to, toFound := ledger.versions[conflict.ToVersion]
	if !fromFound || !toFound || from.ChangeID != to.ChangeID ||
		!transientAmendmentDescendant(ledger, from.ID, to.ID) {
		return errors.New("invalid reconciliation conflict lineage")
	}
	promotionID, promoted := ledger.completed[to.ID]
	if !promoted {
		return ErrVersionNotPromoted
	}
	promotion, found := ledger.promotions[promotionID]
	if !found || conflict.BaseIntent != ledger.current.ID ||
		!transientRevisionDescendsFrom(ledger, ledger.current.ID, promotion.ToIntent) {
		return ErrIntentAdvanced
	}
	descendant, descendantFound := ledger.versions[conflict.Version.ID]
	descendantChange, changeFound := ledger.changes[conflict.Change.ID]
	descendantIDs := ledger.versionIDs[conflict.Change.ID]
	if !descendantFound || !changeFound ||
		len(descendantIDs) == 0 || descendantIDs[len(descendantIDs)-1] != descendant.ID ||
		descendant.ChangeID == from.ChangeID ||
		descendant.BaseIntent != from.BaseIntent ||
		descendant.ChangeID != descendantChange.ID ||
		descendantChange != conflict.Change ||
		!versionsEqual(descendant, conflict.Version) {
		return errors.New("invalid reconciliation descendant version")
	}
	paths, err := NormalizeReconciliationConflictPaths(conflict.AffectedPaths)
	if err != nil || !slices.Equal(paths, conflict.AffectedPaths) {
		return errors.New("invalid reconciliation conflict diagnostics")
	}
	return nil
}

func reconciliationConflictsEqual(left, right ReconciliationConflict) bool {
	return left.ID == right.ID &&
		left.Change == right.Change &&
		left.FromVersion == right.FromVersion &&
		left.ToVersion == right.ToVersion &&
		left.BaseIntent == right.BaseIntent &&
		left.ReportedBy == right.ReportedBy &&
		versionsEqual(left.Version, right.Version) &&
		slices.Equal(left.AffectedPaths, right.AffectedPaths)
}
