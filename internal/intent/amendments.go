package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

var ErrVersionAdvanced = errors.New("change version advanced")
var ErrVersionPromotionStarted = errors.New("change version promotion has started")

type Amendment struct {
	FromVersion VersionID
	ToVersion   VersionID
	Rationale   string
}

type AmendRequest struct {
	IdempotencyKey  string
	ChangeID        ChangeID
	ExpectedVersion VersionID
	Content         ContentRef
	Producer        string
	Rationale       string
}

type Amended struct {
	Change    Change
	Version   Version
	Amendment Amendment
}

func (repository *Repository) Amend(ctx context.Context, request AmendRequest) (Amended, error) {
	if request.IdempotencyKey == "" {
		return Amended{}, errors.New("amendment idempotency key is required")
	}
	if request.ChangeID == "" || request.ExpectedVersion == "" {
		return Amended{}, errors.New("amendment change and expected version are required")
	}
	if request.Content.Engine == "" || request.Content.Revision == "" {
		return Amended{}, errors.New("amended content reference requires engine and revision")
	}
	if request.Producer == "" || request.Rationale == "" {
		return Amended{}, errors.New("amendment producer and rationale are required")
	}

	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()

	existing, found, err := repository.amendments.AmendmentByIdempotencyKey(ctx, request.IdempotencyKey)
	if err != nil {
		return Amended{}, fmt.Errorf("read amendment idempotency record: %w", err)
	}
	if found {
		if existing.Change.ID != request.ChangeID ||
			existing.Amendment.FromVersion != request.ExpectedVersion ||
			existing.Version.Content != request.Content ||
			existing.Version.Producer != request.Producer ||
			existing.Amendment.Rationale != request.Rationale {
			return Amended{}, ErrIdempotencyConflict
		}
		return existing, nil
	}

	change, found, err := repository.changes.Change(ctx, request.ChangeID)
	if err != nil {
		return Amended{}, fmt.Errorf("read amended change: %w", err)
	}
	if !found {
		return Amended{}, ErrChangeNotFound
	}
	previous, found, err := repository.changes.Version(ctx, request.ExpectedVersion)
	if err != nil {
		return Amended{}, fmt.Errorf("read expected amendment version: %w", err)
	}
	if !found || previous.ChangeID != change.ID {
		return Amended{}, ErrVersionNotFound
	}
	latest, found, err := repository.changes.LatestVersion(ctx, change.ID)
	if err != nil {
		return Amended{}, fmt.Errorf("read latest amendment version: %w", err)
	}
	if !found || latest.ID != previous.ID {
		return Amended{}, ErrVersionAdvanced
	}
	if _, promoted, err := repository.promotions.CompletedPromotion(ctx, previous.ID); err != nil {
		return Amended{}, fmt.Errorf("read completed promotion before amendment: %w", err)
	} else if promoted {
		return Amended{}, ErrVersionPromotionStarted
	}
	if pending, found, err := repository.promotions.PendingPromotion(ctx); err != nil {
		return Amended{}, fmt.Errorf("read pending promotion before amendment: %w", err)
	} else if found && pending.Promotion.VersionID == previous.ID {
		return Amended{}, ErrVersionPromotionStarted
	}
	versionID, err := newID("version")
	if err != nil {
		return Amended{}, fmt.Errorf("create amended version id: %w", err)
	}
	version := Version{
		ID:           VersionID(versionID),
		ChangeID:     change.ID,
		BaseIntent:   previous.BaseIntent,
		Content:      request.Content,
		Producer:     request.Producer,
		Dependencies: slices.Clone(previous.Dependencies),
	}
	if err := repository.admission.Admit(ctx, version.ID, version.Content); err != nil {
		return Amended{}, fmt.Errorf("admit amended content: %w", err)
	}
	amendment := Amendment{
		FromVersion: previous.ID,
		ToVersion:   version.ID,
		Rationale:   request.Rationale,
	}
	if err := repository.amendments.RecordAmendment(ctx, request.IdempotencyKey, amendment, version); err != nil {
		return Amended{}, fmt.Errorf("record amendment: %w", err)
	}
	return Amended{Change: change, Version: version, Amendment: amendment}, nil
}
