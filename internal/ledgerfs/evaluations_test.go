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

func TestLedgerRejectsMalformedEvaluatorProvenanceOnWriteAndReplay(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initial := intent.ContentRef{Engine: "fixture", Revision: "content-a"}
	ledger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	repository, err := intent.OpenRepository(ctx, initial, ledger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "provenance-proposal",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "fixture", Revision: "content-b"},
		Producer:       "principal:author",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	evaluation := intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:      "architecture",
			Instruction: "Check the architecture boundary.",
			Assignee:    "principal:architect",
			Provenance: intent.EvaluatorProvenance{
				Evaluator: "fixture-evaluator",
			},
			Reason:   "the change crosses a boundary",
			Evidence: []string{"internal/example.go"},
		}},
	}
	if err := ledger.RecordEvaluation(ctx, evaluation); err == nil {
		t.Fatal("recorded evaluation with partial evaluator provenance")
	}

	evaluation.PolicyEvaluations[0].Provenance.ContractRevision = "fixture/v1"
	if err := ledger.RecordEvaluation(ctx, evaluation); err != nil {
		t.Fatalf("record valid evaluation: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	valid, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	malformed := bytes.Replace(valid, []byte(`"ContractRevision":"fixture/v1"`), []byte(`"ContractRevision":""`), 1)
	if bytes.Equal(malformed, valid) {
		t.Fatal("valid journal did not contain evaluator provenance")
	}
	malformedPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := os.WriteFile(malformedPath, malformed, 0o600); err != nil {
		t.Fatalf("write malformed journal: %v", err)
	}
	if reopened, err := ledgerfs.Open(malformedPath); err == nil {
		_ = reopened.Close()
		t.Fatal("replayed evaluation with partial evaluator provenance")
	}
}

func TestLedgerRestoresEvaluationAndAssignedRequirementWait(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}

	firstLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("open first ledger: %v", err)
	}
	firstRepository, err := intent.OpenRepository(ctx, initial, firstLedger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("open first repository: %v", err)
	}
	proposed, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "evaluated-after-restart",
		BaseIntent:     firstRepository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "principal:contributor",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	evaluation := intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:      "prompts-and-models",
			Instruction: "Does this change modify prompts, LLM usage, or model selection?",
			Assignee:    "principal:models",
			Provenance: intent.EvaluatorProvenance{
				Evaluator:        "exec://example-evaluator/v1",
				ContractRevision: "grd.policy-evaluation/v1",
			},
			RequiresAction: true,
			Reason:         "the candidate changes the model used for booking assistance",
			Evidence:       []string{"internal/booking/prompt.go", "config/models.go"},
		}},
	}
	if _, err := firstRepository.RecordEvaluation(ctx, evaluation); err != nil {
		t.Fatalf("record evaluation: %v", err)
	}
	if err := firstLedger.PreparePromotion(ctx, intent.PreparedPromotion{
		Promotion: intent.Promotion{
			ID:         "promotion_forbidden",
			FromIntent: proposed.Version.BaseIntent,
			ToIntent:   "intent_forbidden",
			VersionID:  proposed.Version.ID,
		},
		Intent: intent.Revision{
			ID:         "intent_forbidden",
			PreviousID: proposed.Version.BaseIntent,
			Content:    proposed.Version.Content,
		},
	}); !errors.Is(err, intent.ErrRequirementRequired) {
		t.Fatalf("prepare promotion behind repository boundary error = %v, want ErrRequirementRequired", err)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatalf("close first ledger: %v", err)
	}

	secondLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen ledger: %v", err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	secondRepository, err := intent.OpenRepository(ctx, initial, secondLedger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	restored, found, err := secondRepository.Evaluation(ctx, proposed.Version.ID)
	if err != nil || !found || !reflect.DeepEqual(restored, evaluation) {
		t.Fatalf("restored evaluation = %#v, %t, %v; want %#v, true, nil", restored, found, err, evaluation)
	}
	pending, err := secondRepository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list restored pending: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("restored pending = %#v, want held Version %q", pending, proposed.Version.ID)
	}
	runnable, err := secondRepository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list restored runnable: %v", err)
	}
	if len(runnable.Versions) != 0 {
		t.Fatalf("restored runnable = %#v, want held Version excluded", runnable)
	}
	beforeRetry, err := os.Stat(journalPath)
	if err != nil {
		t.Fatalf("stat journal before exact retry: %v", err)
	}
	if retried, err := secondRepository.RecordEvaluation(ctx, evaluation); err != nil || !reflect.DeepEqual(retried, evaluation) {
		t.Fatalf("retry restored evaluation = %#v, %v; want %#v, nil", retried, err, evaluation)
	}
	afterRetry, err := os.Stat(journalPath)
	if err != nil {
		t.Fatalf("stat journal after exact retry: %v", err)
	}
	if afterRetry.Size() != beforeRetry.Size() {
		t.Fatalf("journal size after exact retry = %d, want unchanged %d", afterRetry.Size(), beforeRetry.Size())
	}
}

func TestLedgerRestoresAssignedRequirementResponseAndReleasedEvaluation(t *testing.T) {
	ctx := context.Background()
	journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	firstLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	firstRepository, err := intent.OpenRepository(ctx, initial, firstLedger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatal(err)
	}
	proposed, err := firstRepository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "evaluated-after-restart",
		BaseIntent:     firstRepository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "principal:contributor",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = firstRepository.RecordEvaluation(ctx, intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:         "architecture-and-data",
			Instruction:    "Does this change modify architecture or data models?",
			Assignee:       "principal:architecture",
			RequiresAction: true,
			Reason:         "the candidate adds a database",
			Evidence:       []string{"internal/database.go"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	satisfied, err := firstRepository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "durable-satisfaction",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture-and-data",
		Assignee:       "principal:architecture",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "the operational plan is sufficient",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstLedger.Close(); err != nil {
		t.Fatal(err)
	}

	secondLedger, err := ledgerfs.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondLedger.Close() })
	secondRepository, err := intent.OpenRepository(ctx, initial, secondLedger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := secondRepository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "durable-satisfaction",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture-and-data",
		Assignee:       "principal:architecture",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "the operational plan is sufficient",
	})
	if err != nil || !reflect.DeepEqual(retried, satisfied) {
		t.Fatalf("restored satisfaction retry = %#v, %v; want %#v, nil", retried, err, satisfied)
	}
	runnable, err := secondRepository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil || len(runnable.Versions) != 1 || runnable.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("restored runnable = %#v, %v; want satisfied Version", runnable, err)
	}
}

func TestRunnableEvaluationPaginationConformsAcrossTransientAndFilesystemLedgers(t *testing.T) {
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	t.Run("transient", func(t *testing.T) {
		repository, err := intent.NewEphemeralRepository(initial, &recordingAdmission{}, &recordingProjection{current: initial})
		if err != nil {
			t.Fatalf("new transient repository: %v", err)
		}
		want := stageRunnableEvaluationHoles(t, repository)
		assertRunnableEvaluationPages(t, repository, want)
	})

	t.Run("filesystem before and after replay", func(t *testing.T) {
		ctx := context.Background()
		journalPath := filepath.Join(t.TempDir(), "ledger.jsonl")
		firstLedger, err := ledgerfs.Open(journalPath)
		if err != nil {
			t.Fatalf("open first ledger: %v", err)
		}
		projection := &recordingProjection{current: initial}
		firstRepository, err := intent.OpenRepository(ctx, initial, firstLedger, &recordingAdmission{}, projection)
		if err != nil {
			t.Fatalf("open first repository: %v", err)
		}
		want := stageRunnableEvaluationHoles(t, firstRepository)
		assertRunnableEvaluationPages(t, firstRepository, want)
		current := firstRepository.CurrentIntent().Content
		if err := firstLedger.Close(); err != nil {
			t.Fatalf("close first ledger: %v", err)
		}

		secondLedger, err := ledgerfs.Open(journalPath)
		if err != nil {
			t.Fatalf("reopen ledger: %v", err)
		}
		t.Cleanup(func() { _ = secondLedger.Close() })
		secondRepository, err := intent.OpenRepository(ctx, initial, secondLedger, &recordingAdmission{}, &recordingProjection{current: current})
		if err != nil {
			t.Fatalf("reopen repository: %v", err)
		}
		assertRunnableEvaluationPages(t, secondRepository, want)
	})
}

func TestPendingRequirementCursorSurvivesCursorVersionReplacementAcrossLedgers(t *testing.T) {
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	t.Run("transient", func(t *testing.T) {
		repository, err := intent.NewEphemeralRepository(initial, &recordingAdmission{}, &recordingProjection{current: initial})
		if err != nil {
			t.Fatal(err)
		}
		assertRequirementCursorSurvivesReplacement(t, repository)
	})
	t.Run("filesystem", func(t *testing.T) {
		ledger, err := ledgerfs.Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ledger.Close() })
		repository, err := intent.OpenRepository(context.Background(), initial, ledger, &recordingAdmission{}, &recordingProjection{current: initial})
		if err != nil {
			t.Fatal(err)
		}
		assertRequirementCursorSurvivesReplacement(t, repository)
	})
}

func TestLedgerRejectsPromotionPreparedUnderNewerIntentThanEvaluation(t *testing.T) {
	ctx := context.Background()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	ledger, err := ledgerfs.Open(filepath.Join(t.TempDir(), "ledger.jsonl"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	repository, err := intent.OpenRepository(ctx, initial, ledger, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	base := repository.CurrentIntent()
	parent, err := repository.Propose(ctx, intent.Proposal{IdempotencyKey: "stale-parent", BaseIntent: base.ID, Content: intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}, Producer: "principal:contributor"})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{IdempotencyKey: "stale-child", BaseIntent: base.ID, Content: intent.ContentRef{Engine: "git", Revision: "cccccccc"}, Producer: "principal:contributor", Dependencies: []intent.VersionID{parent.Version.ID}})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	if _, err := repository.RecordEvaluation(ctx, intent.Evaluation{
		VersionID: dependent.Version.ID, GoverningIntent: base.ID,
		PolicyEvaluations: []intent.PolicyEvaluation{{Policy: "architecture", Instruction: "Does this change modify architecture?", Assignee: "principal:architecture", Reason: "architecture did not change", Evidence: []string{"no matching semantic change"}}},
	}); err != nil {
		t.Fatalf("record dependent evaluation: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: parent.Version.ID, ExpectedIntent: base.ID}); err != nil {
		t.Fatalf("promote parent: %v", err)
	}
	current := repository.CurrentIntent()
	err = ledger.PreparePromotion(ctx, intent.PreparedPromotion{
		Promotion: intent.Promotion{ID: "promotion_stale", FromIntent: current.ID, ToIntent: "intent_stale", VersionID: dependent.Version.ID},
		Intent:    intent.Revision{ID: "intent_stale", PreviousID: current.ID, Content: dependent.Version.Content},
	})
	if !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("prepare promotion with stale evaluation error = %v, want ErrIntentAdvanced", err)
	}
}

func stageRunnableEvaluationHoles(t *testing.T, repository *intent.Repository) []intent.VersionID {
	t.Helper()
	ctx := context.Background()
	base := repository.CurrentIntent()
	propose := func(name, revision string) intent.Proposed {
		t.Helper()
		proposed, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: "runnable-" + name,
			BaseIntent:     base.ID,
			Content:        intent.ContentRef{Engine: "git", Revision: revision},
			Producer:       "principal:contributor",
		})
		if err != nil {
			t.Fatalf("propose %s: %v", name, err)
		}
		return proposed
	}
	promoted := propose("promoted", "bbbbbbbb")
	held := propose("held", "cccccccc")
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: promoted.Version.ID, ExpectedIntent: base.ID}); err != nil {
		t.Fatalf("promote Version creating pagination hole: %v", err)
	}
	base = repository.CurrentIntent()
	clear := propose("clear", "dddddddd")
	superseded := propose("superseded", "eeeeeeee")
	unevaluated := propose("unevaluated", "ffffffff")
	replacement, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "runnable-replacement",
		ChangeID:        superseded.Change.ID,
		ExpectedVersion: superseded.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "e2e2e2e2"},
		Producer:        "repository-agent@example.com",
		Rationale:       "replace the superseded candidate",
	})
	if err != nil {
		t.Fatalf("replace superseded Version: %v", err)
	}
	record := func(version intent.Version, requiresRequirement bool, reason string) {
		t.Helper()
		_, err := repository.RecordEvaluation(ctx, intent.Evaluation{
			VersionID:       version.ID,
			GoverningIntent: version.BaseIntent,
			PolicyEvaluations: []intent.PolicyEvaluation{{
				Policy:         "architecture",
				Instruction:    "Does this change modify architecture?",
				Assignee:       "principal:architecture",
				RequiresAction: requiresRequirement,
				Reason:         reason,
				Evidence:       []string{"candidate semantic diff"},
			}},
		})
		if err != nil {
			t.Fatalf("record evaluation for %s: %v", version.ID, err)
		}
	}
	record(held.Version, true, "architecture changed")
	record(clear.Version, false, "architecture did not change")
	return []intent.VersionID{clear.Version.ID, unevaluated.Version.ID, replacement.Version.ID}
}

func assertRunnableEvaluationPages(t *testing.T, repository *intent.Repository, want []intent.VersionID) {
	t.Helper()
	ctx := context.Background()
	var got []intent.VersionID
	var cursor intent.VersionID
	for {
		page, err := repository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{After: cursor, Limit: 1})
		if err != nil {
			t.Fatalf("list runnable evaluations after %q: %v", cursor, err)
		}
		if len(page.Versions) != 1 {
			t.Fatalf("runnable page after %q = %#v, want one Version", cursor, page)
		}
		got = append(got, page.Versions[0].ID)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("runnable pages = %q, want %q", got, want)
	}
	if _, err := repository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{After: "version_missing", Limit: 1}); !errors.Is(err, intent.ErrVersionNotFound) {
		t.Fatalf("invalid runnable cursor error = %v, want ErrVersionNotFound", err)
	}
}

func assertRequirementCursorSurvivesReplacement(t *testing.T, repository *intent.Repository) {
	t.Helper()
	ctx := context.Background()
	base := repository.CurrentIntent()
	propose := func(key, revision string) intent.Proposed {
		proposed, err := repository.Propose(ctx, intent.Proposal{
			IdempotencyKey: key,
			BaseIntent:     base.ID,
			Content:        intent.ContentRef{Engine: "git", Revision: revision},
			Producer:       "principal:contributor",
		})
		if err != nil {
			t.Fatal(err)
		}
		return proposed
	}
	first := propose("cursor-first", "bbbbbbbb")
	second := propose("cursor-second", "cccccccc")
	for _, proposed := range []intent.Proposed{first, second} {
		if _, err := repository.RecordEvaluation(ctx, intent.Evaluation{
			VersionID:       proposed.Version.ID,
			GoverningIntent: base.ID,
			PolicyEvaluations: []intent.PolicyEvaluation{{
				Policy:         "architecture",
				Instruction:    "Does this change modify architecture?",
				Assignee:       "principal:architecture",
				RequiresAction: true,
				Reason:         "architecture changed",
				Evidence:       []string{"architecture.go"},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repository.PendingRequirements(ctx, intent.PendingRequirementQuery{Assignee: "principal:architecture", Limit: 1})
	if err != nil || len(page.Requirements) != 1 || page.Requirements[0].VersionID != first.Version.ID {
		t.Fatalf("first requirement page = %#v, %v", page, err)
	}
	if _, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "replace-requirement-cursor",
		ChangeID:        first.Change.ID,
		ExpectedVersion: first.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "dddddddd"},
		Producer:        "repository-agent@example.com",
		Rationale:       "replace the first candidate",
	}); err != nil {
		t.Fatal(err)
	}
	continued, err := repository.PendingRequirements(ctx, intent.PendingRequirementQuery{
		Assignee: "principal:architecture",
		After:    page.NextCursor,
		Limit:    1,
	})
	if err != nil {
		t.Fatalf("continue after superseded requirement cursor: %v", err)
	}
	if len(continued.Requirements) != 1 || continued.Requirements[0].VersionID != second.Version.ID {
		t.Fatalf("continued requirement page = %#v, want Version %q", continued, second.Version.ID)
	}
}
