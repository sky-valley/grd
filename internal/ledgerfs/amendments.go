package ledgerfs

import (
	"context"
	"errors"

	"github.com/sky-valley/grd/internal/intent"
)

func (ledger *Ledger) Amendment(ctx context.Context, toVersion intent.VersionID) (intent.Amendment, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Amendment{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Amendment{}, false, errors.New("journal is closed")
	}
	amendment, found := ledger.state.amendments[toVersion]
	return amendment, found, nil
}

func (ledger *Ledger) AmendmentByIdempotencyKey(ctx context.Context, key string) (intent.Amended, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Amended{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Amended{}, false, errors.New("journal is closed")
	}
	record, found := ledger.state.idempotency[key]
	if !found {
		return intent.Amended{}, false, nil
	}
	if record.operation != amendmentOperation {
		return intent.Amended{}, false, intent.ErrIdempotencyConflict
	}
	versionID := record.versionID
	amendment, amended := ledger.state.amendments[versionID]
	if !amended {
		return intent.Amended{}, false, intent.ErrIdempotencyConflict
	}
	version := cloneVersion(ledger.state.versions[versionID])
	return intent.Amended{
		Change:    ledger.state.changes[version.ChangeID],
		Version:   version,
		Amendment: amendment,
	}, true, nil
}

func (ledger *Ledger) RecordAmendment(ctx context.Context, idempotencyKey string, amendment intent.Amendment, version intent.Version) error {
	return ledger.append(ctx, journalRecord{
		Format:         journalFormat,
		Kind:           amendmentRecorded,
		IdempotencyKey: idempotencyKey,
		Version:        &version,
		Amendment:      &amendment,
	})
}
