package ledgerfs_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestLedgerRestoresHeldVersionDependencies(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}

	firstLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	firstRepository, err := intent.OpenRepository(ctx, initialContent, firstLedger, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("open first repository: %v", err)
	}
	baseIntent := firstRepository.CurrentIntent()
	parent, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "parent",
		BaseIntent:     baseIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "dependent",
		BaseIntent:     baseIntent.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	restored, found, err := secondLedger.Version(ctx, dependent.Version.ID)
	if err != nil {
		t.Fatalf("read restored dependent: %v", err)
	}
	if !found {
		t.Fatal("restored dependent version was not found")
	}
	if !slices.Equal(restored.Dependencies, []intent.VersionID{parent.Version.ID}) {
		t.Fatalf("restored dependencies = %q, want parent version %q", restored.Dependencies, parent.Version.ID)
	}
	restored.Dependencies[0] = "version_corrupted_by_caller"
	again, found, err := secondLedger.Version(ctx, dependent.Version.ID)
	if err != nil || !found {
		t.Fatalf("reread restored dependent = %#v, %t, %v", again, found, err)
	}
	if !slices.Equal(again.Dependencies, []intent.VersionID{parent.Version.ID}) {
		t.Fatalf("stored dependencies changed through returned value: %q", again.Dependencies)
	}
}

func TestLedgerRestoresOnePendingEvaluationForAnIdempotentlyRetriedProposal(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}

	firstLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	firstRepository, err := intent.OpenRepository(ctx, initialContent, firstLedger, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("open first repository: %v", err)
	}
	proposal := intent.Proposal{
		IdempotencyKey: "request-pending",
		BaseIntent:     firstRepository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "contributor",
	}
	proposed, err := firstRepository.Propose(ctx, proposal)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := firstRepository.Propose(ctx, proposal); err != nil {
		t.Fatalf("retry proposal before restart: %v", err)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	secondRepository, err := intent.OpenRepository(ctx, initialContent, secondLedger, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	if _, err := secondRepository.Propose(ctx, proposal); err != nil {
		t.Fatalf("retry proposal after restart: %v", err)
	}

	pending, err := secondRepository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list restored pending evaluations: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("restored pending evaluations = %#v, want version %q once", pending.Versions, proposed.Version.ID)
	}
}

func TestLedgerRestoresPendingEvaluationPaginationAcrossPromotedAndSupersededHoles(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}

	firstLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	firstRepository, err := intent.OpenRepository(ctx, initialContent, firstLedger, &recordingAdmission{}, &recordingProjection{current: initialContent})
	if err != nil {
		t.Fatalf("open first repository: %v", err)
	}
	base := firstRepository.CurrentIntent()
	proposals := make([]intent.Proposed, 0, 3)
	for index, revision := range []string{"bbbbbbbb", "cccccccc", "dddddddd"} {
		proposed, err := firstRepository.Propose(ctx, intent.Proposal{
			IdempotencyKey: "pending-hole-" + revision,
			BaseIntent:     base.ID,
			Content:        intent.ContentRef{Engine: "git", Revision: revision},
			Producer:       "actor-" + string(rune('a'+index)),
		})
		if err != nil {
			t.Fatalf("propose %s: %v", revision, err)
		}
		proposals = append(proposals, proposed)
	}
	amended, err := firstRepository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "pending-hole-amend-second",
		ChangeID:        proposals[1].Change.ID,
		ExpectedVersion: proposals[1].Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "c2c2c2c2"},
		Producer:        "repository-agent",
		Rationale:       "replace the second pending version",
	})
	if err != nil {
		t.Fatalf("amend second proposal: %v", err)
	}
	if _, err := firstRepository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposals[0].Version.ID,
		ExpectedIntent: base.ID,
	}); err != nil {
		t.Fatalf("promote first proposal: %v", err)
	}

	want := []intent.VersionID{proposals[2].Version.ID, amended.Version.ID}
	assertPendingEvaluationPages(t, firstRepository, want)
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	secondRepository, err := intent.OpenRepository(ctx, initialContent, secondLedger, &recordingAdmission{}, &recordingProjection{current: proposals[0].Version.Content})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	assertPendingEvaluationPages(t, secondRepository, want)
}

func assertPendingEvaluationPages(t *testing.T, repository *intent.Repository, want []intent.VersionID) {
	t.Helper()
	ctx := context.Background()
	var got []intent.VersionID
	var cursor intent.VersionID
	for {
		page, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{After: cursor, Limit: 1})
		if err != nil {
			t.Fatalf("list pending evaluations after %q: %v", cursor, err)
		}
		if len(page.Versions) != 1 {
			t.Fatalf("pending evaluation page after %q = %#v, want one version", cursor, page)
		}
		got = append(got, page.Versions[0].ID)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("pending evaluation pages = %q, want %q", got, want)
	}
}

func TestRepositoryRestoresIntentAndProposalIdempotencyFromJournal(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initialContent := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	proposedContent := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}

	firstLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	firstAdmission := &recordingAdmission{}
	firstProjection := &recordingProjection{current: initialContent}
	firstRepository, err := intent.OpenRepository(ctx, initialContent, firstLedger, firstAdmission, firstProjection)
	if err != nil {
		t.Fatalf("open first repository: %v", err)
	}
	initialIntent := firstRepository.CurrentIntent()
	proposal := intent.Proposal{
		IdempotencyKey: "request-1",
		BaseIntent:     initialIntent.ID,
		Content:        proposedContent,
		Producer:       "contributor",
	}
	proposed, err := firstRepository.Propose(ctx, proposal)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	promoted, err := firstRepository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: initialIntent.ID,
	})
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() {
		if err := secondLedger.Close(); err != nil {
			t.Errorf("close second ledger: %v", err)
		}
	})
	secondAdmission := &recordingAdmission{}
	secondRepository, err := intent.OpenRepository(ctx, initialContent, secondLedger, secondAdmission, &recordingProjection{current: proposedContent})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	if got := secondRepository.CurrentIntent(); got != promoted.Intent {
		t.Fatalf("restored current intent = %#v, want %#v", got, promoted.Intent)
	}

	retried, err := secondRepository.Propose(ctx, proposal)
	if err != nil {
		t.Fatalf("retry proposal: %v", err)
	}
	if !reflect.DeepEqual(retried, proposed) {
		t.Fatalf("retried proposal = %#v, want %#v", retried, proposed)
	}
	if len(secondAdmission.admissions) != 0 {
		t.Fatalf("content admissions on idempotent retry = %d, want 0", len(secondAdmission.admissions))
	}
	conflictingProposal := proposal
	conflictingProposal.Content = intent.ContentRef{Engine: "git", Revision: "cccccccc"}
	if _, err := secondRepository.Propose(ctx, conflictingProposal); !errors.Is(err, intent.ErrIdempotencyConflict) {
		t.Fatalf("conflicting proposal retry error = %v, want ErrIdempotencyConflict", err)
	}

	storedIntent, found, err := secondLedger.CurrentIntent(ctx)
	if err != nil {
		t.Fatalf("read stored current intent: %v", err)
	}
	if !found || storedIntent != promoted.Intent {
		t.Fatalf("stored current intent = %#v, %t; want %#v, true", storedIntent, found, promoted.Intent)
	}
	storedVersion, found, err := secondLedger.Version(ctx, proposed.Version.ID)
	if err != nil {
		t.Fatalf("read stored version: %v", err)
	}
	if !found || !reflect.DeepEqual(storedVersion, proposed.Version) {
		t.Fatalf("stored version = %#v, %t; want %#v, true", storedVersion, found, proposed.Version)
	}
	storedChange, found, err := secondLedger.Change(ctx, proposed.Change.ID)
	if err != nil {
		t.Fatalf("read stored change: %v", err)
	}
	if !found || storedChange != proposed.Change {
		t.Fatalf("stored change = %#v, %t; want %#v, true", storedChange, found, proposed.Change)
	}
	latestVersion, found, err := secondLedger.LatestVersion(ctx, proposed.Change.ID)
	if err != nil {
		t.Fatalf("read latest stored version: %v", err)
	}
	if !found || !reflect.DeepEqual(latestVersion, proposed.Version) {
		t.Fatalf("latest stored version = %#v, %t; want %#v, true", latestVersion, found, proposed.Version)
	}
	versions, more, err := secondLedger.Versions(ctx, proposed.Change.ID, "", 1)
	if err != nil {
		t.Fatalf("read stored versions: %v", err)
	}
	if len(versions) != 1 || !reflect.DeepEqual(versions[0], proposed.Version) || more {
		t.Fatalf("stored versions = %#v, more %t; want [%#v], false", versions, more, proposed.Version)
	}
	storedProposal, found, err := secondLedger.ProposalByIdempotencyKey(ctx, proposal.IdempotencyKey)
	if err != nil {
		t.Fatalf("read stored idempotent proposal: %v", err)
	}
	if !found || !reflect.DeepEqual(storedProposal, proposed) {
		t.Fatalf("stored proposal = %#v, %t; want %#v, true", storedProposal, found, proposed)
	}
}

func TestLedgerRecoversIncompleteFinalRecord(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	initial := intent.Revision{
		ID:      "intent_initial",
		Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"},
	}
	if err := ledger.Initialize(ctx, initial); err != nil {
		t.Fatalf("initialize ledger: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	file, err := os.OpenFile(journalPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open journal tail: %v", err)
	}
	if _, err := file.WriteString(`{"format":1,"kind":"proposal_recorded"`); err != nil {
		t.Fatalf("write incomplete record: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close journal tail: %v", err)
	}

	reopened, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	current, found, err := reopened.CurrentIntent(ctx)
	if err != nil {
		t.Fatalf("read recovered current intent: %v", err)
	}
	if !found || current != initial {
		t.Fatalf("recovered current intent = %#v, %t; want %#v, true", current, found, initial)
	}
}

func TestLedgerAllowsOnlyOneWriter(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	first, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	if second, err := ledgerfs.Open(journalPath); err == nil {
		_ = second.Close()
		t.Fatal("opened a second writer for the same journal")
	}
}

func TestLedgerRejectsTrailingDataAfterACompleteRecord(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	if err := ledger.Initialize(ctx, intent.Revision{
		ID:      "intent_initial",
		Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"},
	}); err != nil {
		t.Fatalf("initialize ledger: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	record, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	record = bytes.TrimSuffix(record, []byte{'\n'})
	corrupt := append(append(append([]byte(nil), record...), ' '), record...)
	corrupt = append(corrupt, '\n')
	if err := os.WriteFile(journalPath, corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt journal: %v", err)
	}
	if reopened, err := ledgerfs.Open(journalPath); err == nil {
		_ = reopened.Close()
		t.Fatal("opened journal with trailing data after a complete record")
	}
}

type recordingAdmission struct {
	admissions []intent.VersionID
}

func (admission *recordingAdmission) Admit(_ context.Context, versionID intent.VersionID, _ intent.ContentRef) error {
	admission.admissions = append(admission.admissions, versionID)
	return nil
}

type recordingProjection struct {
	current intent.ContentRef
}

func (projection *recordingProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *recordingProjection) Advance(_ context.Context, _, next intent.ContentRef) error {
	projection.current = next
	return nil
}
