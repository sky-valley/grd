package intent

import (
	"context"
	"testing"
)

func TestTransientLedgerRejectsDependentReconciliationWithoutAmendmentLineage(t *testing.T) {
	ctx := context.Background()
	initialContent := ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	ledger := &transientLedger{}
	repository, err := OpenRepository(ctx, initialContent, ledger, acceptingLedgerTestAdmission{}, &ledgerTestProjection{current: initialContent})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	parent, err := repository.Propose(ctx, Proposal{
		IdempotencyKey: "proposal-b",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, Proposal{
		IdempotencyKey: "proposal-c",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	unrelated, err := repository.Propose(ctx, Proposal{
		IdempotencyKey: "proposal-d",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:       "maintainer",
	})
	if err != nil {
		t.Fatalf("propose unrelated change: %v", err)
	}
	promoted, err := repository.Promote(ctx, PromoteRequest{
		VersionID:      unrelated.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	})
	if err != nil {
		t.Fatalf("promote unrelated change: %v", err)
	}
	version := Version{
		ID:         "version_c_prime",
		ChangeID:   dependent.Change.ID,
		BaseIntent: promoted.Intent.ID,
		Content:    ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:   "git-engine",
	}
	reconciliation := DependentReconciliation{
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

type acceptingLedgerTestAdmission struct{}

func (acceptingLedgerTestAdmission) Admit(context.Context, VersionID, ContentRef) error {
	return nil
}

type ledgerTestProjection struct {
	current ContentRef
}

func (projection *ledgerTestProjection) Current(context.Context) (ContentRef, error) {
	return projection.current, nil
}

func (projection *ledgerTestProjection) Advance(_ context.Context, _ ContentRef, next ContentRef) error {
	projection.current = next
	return nil
}
