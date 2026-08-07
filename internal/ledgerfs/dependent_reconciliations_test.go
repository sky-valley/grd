package ledgerfs_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestLedgerRestoresDependentReconciliationAndItsIdempotency(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}

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
	parent, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     firstRepository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     firstRepository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	amended, err := firstRepository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: parent.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "repair B",
	})
	if err != nil {
		t.Fatalf("amend parent: %v", err)
	}
	promoted, err := firstRepository.Promote(ctx, intent.PromoteRequest{
		VersionID:      amended.Version.ID,
		ExpectedIntent: firstRepository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote amended parent: %v", err)
	}
	request := intent.ReconcileDependentRequest{
		IdempotencyKey:     "reconcile-c",
		ExpectedVersion:    dependent.Version.ID,
		ReplacedDependency: parent.Version.ID,
		AcceptedVersion:    amended.Version.ID,
		ExpectedIntent:     promoted.Intent.ID,
		Content:            intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:           "git-engine",
		Rationale:          "replay C onto accepted B prime",
	}
	reconciled, err := firstRepository.ReconcileDependent(ctx, request)
	if err != nil {
		t.Fatalf("reconcile dependent: %v", err)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	secondAdmission := &recordingAdmission{}
	secondRepository, err := intent.OpenRepository(
		ctx,
		initialContent,
		secondLedger,
		secondAdmission,
		&recordingProjection{current: amended.Version.Content},
	)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	retried, err := secondRepository.ReconcileDependent(ctx, request)
	if err != nil {
		t.Fatalf("retry dependent reconciliation: %v", err)
	}
	if !reflect.DeepEqual(retried, reconciled) {
		t.Fatalf("retried reconciliation = %#v, want %#v", retried, reconciled)
	}
	if len(secondAdmission.admissions) != 0 {
		t.Fatalf("content admissions on idempotent retry = %d, want 0", len(secondAdmission.admissions))
	}
	stored, found, err := secondLedger.DependentReconciliation(ctx, reconciled.Version.ID)
	if err != nil {
		t.Fatalf("read stored reconciliation: %v", err)
	}
	if !found || stored != reconciled.Reconciliation {
		t.Fatalf("stored reconciliation = %#v, %t; want %#v, true", stored, found, reconciled.Reconciliation)
	}
}

func TestLedgerRejectsDependentReconciliationWithoutAmendmentLineage(t *testing.T) {
	ctx := context.Background()
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	ledger, err := ledgerfs.Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	repository, err := intent.OpenRepository(
		ctx,
		initialContent,
		ledger,
		&recordingAdmission{},
		&recordingProjection{current: initialContent},
	)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose unrelated change: %v", err)
	}
	promoted, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote unrelated change: %v", err)
	}
	version := intent.Version{
		ID:         "version_c_prime",
		ChangeID:   dependent.Change.ID,
		BaseIntent: promoted.Intent.ID,
		Content:    intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:   "git-engine",
	}
	reconciliation := intent.DependentReconciliation{
		FromVersion:        dependent.Version.ID,
		ToVersion:          version.ID,
		ReplacedDependency: parent.Version.ID,
		AcceptedVersion:    unrelated.Version.ID,
		BaseIntent:         promoted.Intent.ID,
		Rationale:          "invalid replacement",
	}
	if err := ledger.RecordDependentReconciliation(ctx, "invalid-reconciliation", reconciliation, version); err == nil {
		t.Fatal("malformed reconciliation was accepted without B to B prime amendment lineage")
	}
}

func TestLedgerRestoresHistoricalDependentConflictWithoutRequeueingCandidate(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	repository, err := intent.OpenRepository(
		ctx,
		initialContent,
		ledger,
		&recordingAdmission{},
		&recordingProjection{current: initialContent},
	)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose B: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose C: %v", err)
	}
	firstAmendment, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b-1",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: parent.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b1b1b1b1"},
		Producer:        "repository-agent",
		Rationale:       "first repair",
	})
	if err != nil {
		t.Fatalf("first amendment: %v", err)
	}
	finalAmendment, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-b-2",
		ChangeID:        parent.Change.ID,
		ExpectedVersion: firstAmendment.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "b2b2b2b2"},
		Producer:        "repository-agent",
		Rationale:       "final repair",
	})
	if err != nil {
		t.Fatalf("final amendment: %v", err)
	}
	acceptedParent, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      finalAmendment.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote B2: %v", err)
	}
	unrelated, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-d",
		BaseIntent:     acceptedParent.Intent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose D: %v", err)
	}
	current, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: acceptedParent.Intent.ID,
	})
	if err != nil {
		t.Fatalf("promote D: %v", err)
	}
	conflict, err := repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    "conflict-c-after-d",
		FromVersion:       parent.Version.ID,
		ToVersion:         finalAmendment.Version.ID,
		DescendantVersion: dependent.Version.ID,
		ExpectedIntent:    current.Intent.ID,
		ReportedBy:        "git-engine",
		AffectedPaths:     []string{"schema.sql"},
	})
	if err != nil {
		t.Fatalf("record conflict: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	reopened, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := intent.OpenRepository(
		ctx,
		initialContent,
		reopened,
		&recordingAdmission{},
		&recordingProjection{current: unrelated.Version.Content},
	)
	if err != nil {
		t.Fatalf("restart repository: %v", err)
	}
	restored, found, err := restarted.ReconciliationConflict(ctx, conflict.ID)
	if err != nil {
		t.Fatalf("read restored conflict: %v", err)
	}
	if !found || restored.BaseIntent != current.Intent.ID {
		t.Fatalf("restored conflict = %#v, %t; want conflict against D", restored, found)
	}
	candidates, err := restarted.DependentReconciliations(ctx)
	if err != nil {
		t.Fatalf("read candidates after restart: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates after restored conflict = %#v, want durable conflict to own evaluation work", candidates)
	}
}
