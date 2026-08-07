package gitengine_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sky-valley/grd/internal/gitengine"
	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestRepositoryRestartCompletesPromotionAlreadyProjectedByGit(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	gitRepository, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open git repository: %v", err)
	}
	completionErr := errors.New("journal completion unavailable")
	repository, err := intent.OpenRepository(
		ctx,
		intent.ContentRef{Engine: "git", Revision: fixture.initial},
		&failingCompleteLedger{Ledger: ledger, err: completionErr},
		gitRepository,
		gitRepository,
	)
	if err != nil {
		t.Fatalf("open intent repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: fixture.proposed},
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
	pendingEvaluations, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluations: %v", err)
	}
	if len(pendingEvaluations.Versions) != 0 {
		t.Fatalf("pending evaluations = %#v, want none after promotion was prepared", pendingEvaluations.Versions)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.proposed {
		t.Fatalf("trunk after interrupted promotion = %q, want %q", got, fixture.proposed)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	reopened, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedGit, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("reopen git repository: %v", err)
	}
	restarted, err := intent.OpenRepository(
		ctx,
		intent.ContentRef{Engine: "git", Revision: fixture.initial},
		reopened,
		restartedGit,
		restartedGit,
	)
	if err != nil {
		t.Fatalf("restart intent repository: %v", err)
	}
	if got := restarted.CurrentIntent(); got != prepared.Intent {
		t.Fatalf("restarted intent = %#v, want %#v", got, prepared.Intent)
	}
	completed, found, err := reopened.CompletedPromotion(ctx, proposed.Version.ID)
	if err != nil || !found || completed.Promotion != prepared.Promotion || completed.Intent != prepared.Intent {
		t.Fatalf("completed promotion = %#v, %t, error %v; want %#v, true, nil", completed, found, err, prepared)
	}
}

func TestRepositoryRestartRetriesPromotionNotYetProjectedByGit(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	gitRepository, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open git repository: %v", err)
	}
	projectionErr := errors.New("projection unavailable")
	repository, err := intent.OpenRepository(
		ctx,
		intent.ContentRef{Engine: "git", Revision: fixture.initial},
		ledger,
		gitRepository,
		&failingAdvanceProjection{TrunkProjection: gitRepository, err: projectionErr},
	)
	if err != nil {
		t.Fatalf("open intent repository: %v", err)
	}
	initialIntent := repository.CurrentIntent()
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: fixture.proposed},
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
		t.Fatalf("promote error = %v, want projection error", err)
	}
	prepared, found, err := ledger.PendingPromotion(ctx)
	if err != nil || !found {
		t.Fatalf("pending promotion = %#v, %t, error %v; want prepared, true, nil", prepared, found, err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.initial {
		t.Fatalf("trunk before restart = %q, want %q", got, fixture.initial)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	reopened, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedGit, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("reopen git repository: %v", err)
	}
	restarted, err := intent.OpenRepository(
		ctx,
		intent.ContentRef{Engine: "git", Revision: fixture.initial},
		reopened,
		restartedGit,
		restartedGit,
	)
	if err != nil {
		t.Fatalf("restart intent repository: %v", err)
	}
	if got := restarted.CurrentIntent(); got != prepared.Intent {
		t.Fatalf("restarted intent = %#v, want %#v", got, prepared.Intent)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.proposed {
		t.Fatalf("trunk after restart = %q, want %q", got, fixture.proposed)
	}
}

type failingCompleteLedger struct {
	intent.Ledger
	err error
}

func (ledger *failingCompleteLedger) CompletePromotion(context.Context, intent.PromotionID) error {
	return ledger.err
}

type failingAdvanceProjection struct {
	intent.TrunkProjection
	err error
}

func (projection *failingAdvanceProjection) Advance(context.Context, intent.ContentRef, intent.ContentRef) error {
	return projection.err
}
