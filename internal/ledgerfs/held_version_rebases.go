package ledgerfs

import (
	"context"
	"errors"
	"slices"

	"github.com/sky-valley/grd/internal/intent"
)

func validateHeldVersionRebase(state *journalState, record journalRecord) error {
	if state.current.ID == "" {
		return errors.New("held version rebase precedes repository initialization")
	}
	if record.IdempotencyKey == "" || record.HeldVersionRebase == nil || record.Version == nil {
		return errors.New("invalid held version rebase record")
	}
	rebase := *record.HeldVersionRebase
	version := *record.Version
	if existing, ok := state.idempotency[record.IdempotencyKey]; ok {
		if existing.operation != heldVersionRebaseOperation {
			return intent.ErrIdempotencyConflict
		}
		storedRebase, rebaseFound := state.rebases[existing.versionID]
		storedVersion, versionFound := state.versions[existing.versionID]
		if rebaseFound && versionFound && storedRebase == rebase && sameVersion(storedVersion, version) {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	previous, found := state.versions[rebase.FromVersion]
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
	if rebase.ToVersion == "" || rebase.ToVersion != version.ID ||
		rebase.FromIntent != previous.BaseIntent ||
		rebase.ToIntent == "" || rebase.ToIntent != state.current.ID ||
		rebase.FromIntent == rebase.ToIntent || rebase.Rationale == "" {
		return errors.New("invalid held version rebase identity")
	}
	if !recordedRevisionDescendsFrom(state, rebase.ToIntent, rebase.FromIntent) {
		return intent.ErrIntentAdvanced
	}
	if version.ID == "" || version.ChangeID != previous.ChangeID || version.BaseIntent != rebase.ToIntent ||
		version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid rebased held version")
	}
	if !slices.Equal(version.Dependencies, previous.Dependencies) {
		return errors.New("rebased held version does not preserve dependencies")
	}
	if _, found := state.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}

func applyHeldVersionRebase(state *journalState, record journalRecord) {
	if _, exists := state.idempotency[record.IdempotencyKey]; exists {
		return
	}
	version := cloneVersion(*record.Version)
	rebase := *record.HeldVersionRebase
	state.versions[version.ID] = version
	state.versionIDs[version.ChangeID] = append(state.versionIDs[version.ChangeID], version.ID)
	for _, dependencyID := range version.Dependencies {
		state.dependents[dependencyID] = append(state.dependents[dependencyID], version.ID)
	}
	state.rebases[version.ID] = rebase
	state.idempotency[record.IdempotencyKey] = idempotencyRecord{
		operation: heldVersionRebaseOperation,
		versionID: version.ID,
	}
	beginPendingEvaluation(state, version.ID, rebase.FromVersion)
}

func (ledger *Ledger) HeldVersionRebase(ctx context.Context, toVersion intent.VersionID) (intent.HeldVersionRebase, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.HeldVersionRebase{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.HeldVersionRebase{}, false, errors.New("journal is closed")
	}
	rebase, found := ledger.state.rebases[toVersion]
	return rebase, found, nil
}

func (ledger *Ledger) HeldVersionRebaseByIdempotencyKey(ctx context.Context, key string) (intent.RebasedHeldVersion, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.RebasedHeldVersion{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.RebasedHeldVersion{}, false, errors.New("journal is closed")
	}
	record, found := ledger.state.idempotency[key]
	if !found {
		return intent.RebasedHeldVersion{}, false, nil
	}
	if record.operation != heldVersionRebaseOperation {
		return intent.RebasedHeldVersion{}, false, intent.ErrIdempotencyConflict
	}
	rebase, rebased := ledger.state.rebases[record.versionID]
	version, versionFound := ledger.state.versions[record.versionID]
	change, changeFound := ledger.state.changes[version.ChangeID]
	if !rebased || !versionFound || !changeFound {
		return intent.RebasedHeldVersion{}, false, intent.ErrIdempotencyConflict
	}
	return intent.RebasedHeldVersion{Change: change, Version: cloneVersion(version), Rebase: rebase}, true, nil
}

func (ledger *Ledger) RecordHeldVersionRebase(
	ctx context.Context,
	key string,
	rebase intent.HeldVersionRebase,
	version intent.Version,
) error {
	copy := cloneVersion(version)
	return ledger.append(ctx, journalRecord{
		Format:            journalFormat,
		Kind:              heldVersionRebaseRecorded,
		IdempotencyKey:    key,
		Version:           &copy,
		HeldVersionRebase: &rebase,
	})
}
