package ledgerfs_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestLedgerRestoresHeldVersionRebaseAndExactRetry(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	firstLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	firstRepository, err := intent.OpenRepository(
		ctx,
		initialContent,
		firstLedger,
		&recordingAdmission{},
		&recordingProjection{current: initialContent},
	)
	if err != nil {
		t.Fatalf("open first repository: %v", err)
	}
	held, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-held",
		BaseIntent:     firstRepository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose held change: %v", err)
	}
	unrelated, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-current",
		BaseIntent:     firstRepository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose current change: %v", err)
	}
	current, err := firstRepository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: firstRepository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote current change: %v", err)
	}
	request := intent.RebaseHeldVersionRequest{
		IdempotencyKey:  "rebase-held",
		ExpectedVersion: held.Version.ID,
		ExpectedIntent:  current.Intent.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:        "repository-engine",
		Rationale:       "replay held change onto current intent",
	}
	rebased, err := firstRepository.RebaseHeldVersion(ctx, request)
	if err != nil {
		t.Fatalf("rebase held version: %v", err)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	admission := &recordingAdmission{}
	secondRepository, err := intent.OpenRepository(
		ctx,
		initialContent,
		secondLedger,
		admission,
		&recordingProjection{current: current.Intent.Content},
	)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	retried, err := secondRepository.RebaseHeldVersion(ctx, request)
	if err != nil {
		t.Fatalf("retry held version rebase: %v", err)
	}
	if !reflect.DeepEqual(retried, rebased) {
		t.Fatalf("retried held version rebase = %#v, want %#v", retried, rebased)
	}
	if len(admission.admissions) != 0 {
		t.Fatalf("content admissions on idempotent retry = %d, want 0", len(admission.admissions))
	}
	stored, found, err := secondLedger.HeldVersionRebase(ctx, rebased.Version.ID)
	if err != nil {
		t.Fatalf("read stored held version rebase: %v", err)
	}
	if !found || stored != rebased.Rebase {
		t.Fatalf("stored held version rebase = %#v, %t; want %#v, true", stored, found, rebased.Rebase)
	}
}
