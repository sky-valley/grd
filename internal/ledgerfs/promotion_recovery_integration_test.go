package ledgerfs_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestRepositoryRetriesThePreparedPromotion(t *testing.T) {
	ctx := context.Background()
	ledger, err := ledgerfs.Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	proposedContent := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	projectionErr := errors.New("projection unavailable")
	projection := &promotionRecordingProjection{current: initialContent, err: projectionErr}
	repository, err := intent.OpenRepository(ctx, initialContent, ledger, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        proposedContent,
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("first promote error = %v, want projection error", err)
	}
	prepared, found, err := ledger.PendingPromotion(ctx)
	if err != nil {
		t.Fatalf("read pending promotion: %v", err)
	}
	if !found {
		t.Fatal("prepared promotion was not retained")
	}
	if got := repository.CurrentIntent(); got != initialIntent {
		t.Fatalf("current intent before completion = %#v, want %#v", got, initialIntent)
	}

	projection.err = nil
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if err != nil {
		t.Fatalf("retry promote: %v", err)
	}
	if promoted.Promotion != prepared.Promotion || promoted.Intent != prepared.Intent {
		t.Fatalf("retried promotion = %#v, want prepared %#v", promoted, prepared)
	}
	if _, found, err := ledger.PendingPromotion(ctx); err != nil || found {
		t.Fatalf("pending promotion after completion = found %t, error %v; want false, nil", found, err)
	}
	stored, found, err := ledger.CompletedPromotion(ctx, proposed.Version.ID)
	if err != nil {
		t.Fatalf("read completed promotion: %v", err)
	}
	if !found || stored != promoted {
		t.Fatalf("stored promotion = %#v, %t; want %#v, true", stored, found, promoted)
	}
}

func TestRepositoryCompletesPreparedPromotionAfterTrunkMoved(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	proposedContent := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	projection := &promotionRecordingProjection{current: initialContent}
	completionErr := errors.New("journal completion unavailable")
	repository, err := intent.OpenRepository(ctx, initialContent, &failingCompleteLedger{
		Ledger: ledger,
		err:    completionErr,
	}, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        proposedContent,
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if !errors.Is(err, completionErr) {
		t.Fatalf("promote error = %v, want completion error", err)
	}
	prepared, found, err := ledger.PendingPromotion(ctx)
	if err != nil || !found {
		t.Fatalf("pending promotion = %#v, %t, error %v; want prepared, true, nil", prepared, found, err)
	}
	if projection.current != proposedContent {
		t.Fatalf("trunk after incomplete promotion = %#v, want %#v", projection.current, proposedContent)
	}
	if got := repository.CurrentIntent(); got != initialIntent {
		t.Fatalf("intent after incomplete promotion = %#v, want %#v", got, initialIntent)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	reopened, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := intent.OpenRepository(ctx, initialContent, reopened, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("restart repository: %v", err)
	}
	if got := restarted.CurrentIntent(); got != prepared.Intent {
		t.Fatalf("intent after restart = %#v, want %#v", got, prepared.Intent)
	}
	if len(projection.advances) != 1 {
		t.Fatalf("projection advances = %d, want the original advance only", len(projection.advances))
	}
	stored, found, err := reopened.CompletedPromotion(ctx, proposed.Version.ID)
	if err != nil || !found || stored.Promotion != prepared.Promotion || stored.Intent != prepared.Intent {
		t.Fatalf("completed promotion after restart = %#v, %t, error %v; want %#v, true, nil", stored, found, err, prepared)
	}
}

func TestPromotionRetryRepairsCurrentIntentAfterAmbiguousCompletion(t *testing.T) {
	ctx := context.Background()
	ledger, err := ledgerfs.Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	proposedContent := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	projection := &promotionRecordingProjection{current: initialContent}
	completionErr := errors.New("completion result lost")
	repository, err := intent.OpenRepository(ctx, initialContent, &ambiguousCompleteLedger{
		Ledger: ledger,
		err:    completionErr,
	}, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        proposedContent,
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if !errors.Is(err, completionErr) {
		t.Fatalf("promote error = %v, want ambiguous completion error", err)
	}
	completed, found, err := ledger.CompletedPromotion(ctx, proposed.Version.ID)
	if err != nil || !found {
		t.Fatalf("durable completed promotion = %#v, %t, error %v; want completed, true, nil", completed, found, err)
	}
	if got := repository.CurrentIntent(); got != initialIntent {
		t.Fatalf("cached intent before retry = %#v, want %#v", got, initialIntent)
	}

	retried, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if err != nil {
		t.Fatalf("retry promotion: %v", err)
	}
	if retried != completed {
		t.Fatalf("retried promotion = %#v, want %#v", retried, completed)
	}
	if got := repository.CurrentIntent(); got != completed.Intent {
		t.Fatalf("cached intent after retry = %#v, want %#v", got, completed.Intent)
	}
}

func TestRepositoryExposesUnexpectedTrunkWithoutOverwritingIt(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	proposedContent := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	unexpectedContent := intent.ContentRef{Engine: "git", Revision: "cccccccc"}
	projection := &promotionRecordingProjection{current: unexpectedContent}
	repository, err := intent.OpenRepository(ctx, initialContent, ledger, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        proposedContent,
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	var conflict *intent.ProjectionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("promote error = %v, want ProjectionConflict", err)
	}
	if conflict.Expected != initialContent || conflict.Prepared.Intent.Content != proposedContent || conflict.Actual != unexpectedContent {
		t.Fatalf("reconciliation conflict = %#v, want expected A, target B, actual C", conflict)
	}
	if projection.current != unexpectedContent || len(projection.advances) != 0 {
		t.Fatalf("projection after conflict = %#v with %d advances; want C with 0", projection.current, len(projection.advances))
	}
	if got := repository.CurrentIntent(); got != initialIntent {
		t.Fatalf("intent after conflict = %#v, want %#v", got, initialIntent)
	}
	if stored, found := repository.ProjectionConflict(); !found || stored != *conflict {
		t.Fatalf("exposed conflict = %#v, %t; want %#v, true", stored, found, *conflict)
	}
	prepared, found, err := ledger.PendingPromotion(ctx)
	if err != nil || !found || prepared != conflict.Prepared {
		t.Fatalf("pending promotion after conflict = %#v, %t, error %v; want %#v, true, nil", prepared, found, err, conflict.Prepared)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	reopened, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := intent.OpenRepository(ctx, initialContent, reopened, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("restart conflicted repository: %v", err)
	}
	if stored, found := restarted.ProjectionConflict(); !found || stored != *conflict {
		t.Fatalf("restarted conflict = %#v, %t; want %#v, true", stored, found, *conflict)
	}
	if projection.current != unexpectedContent || len(projection.advances) != 0 {
		t.Fatalf("projection after conflicted restart = %#v with %d advances; want C with 0", projection.current, len(projection.advances))
	}
}

func TestRepositoryClassifiesConcurrentTrunkAdvanceImmediately(t *testing.T) {
	ctx := context.Background()
	ledger, err := ledgerfs.Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	proposedContent := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	concurrentContent := intent.ContentRef{Engine: "git", Revision: "cccccccc"}
	projection := &racingProjection{current: initialContent, concurrent: concurrentContent}
	repository, err := intent.OpenRepository(ctx, initialContent, ledger, &recordingAdmission{}, projection)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        proposedContent,
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	_, err = repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	var conflict *intent.ProjectionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("promote error = %v, want ProjectionConflict", err)
	}
	if conflict.Expected != initialContent || conflict.Prepared.Intent.Content != proposedContent || conflict.Actual != concurrentContent {
		t.Fatalf("reconciliation conflict = %#v, want expected A, target B, actual C", conflict)
	}
	if projection.current != concurrentContent || projection.advances != 1 {
		t.Fatalf("projection after race = %#v with %d advances; want C with one rejected CAS", projection.current, projection.advances)
	}
	if stored, found := repository.ProjectionConflict(); !found || stored != *conflict {
		t.Fatalf("exposed conflict = %#v, %t; want %#v, true", stored, found, *conflict)
	}
}

type failingCompleteLedger struct {
	intent.Ledger
	err error
}

type promotionRecordingProjection struct {
	current  intent.ContentRef
	advances []promotionProjectionAdvance
	err      error
}

type promotionProjectionAdvance struct {
	expected intent.ContentRef
	next     intent.ContentRef
}

func (projection *promotionRecordingProjection) Advance(_ context.Context, expected, next intent.ContentRef) error {
	projection.advances = append(projection.advances, promotionProjectionAdvance{expected: expected, next: next})
	if projection.err == nil {
		projection.current = next
	}
	return projection.err
}

func (projection *promotionRecordingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (ledger *failingCompleteLedger) CompletePromotion(context.Context, intent.PromotionID) error {
	return ledger.err
}

type ambiguousCompleteLedger struct {
	intent.Ledger
	err error
}

func (ledger *ambiguousCompleteLedger) CompletePromotion(ctx context.Context, promotionID intent.PromotionID) error {
	if err := ledger.Ledger.CompletePromotion(ctx, promotionID); err != nil {
		return err
	}
	return ledger.err
}

type racingProjection struct {
	current    intent.ContentRef
	concurrent intent.ContentRef
	advances   int
}

func (projection *racingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *racingProjection) Advance(context.Context, intent.ContentRef, intent.ContentRef) error {
	projection.advances++
	projection.current = projection.concurrent
	return intent.ErrIntentAdvanced
}
