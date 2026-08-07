package ledgerfs_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestJournalV1DecisionLoopFormat(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger, err := ledgerfs.Open(path)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}

	initial := intent.Revision{
		ID:      "intent_initial",
		Content: intent.ContentRef{Engine: "fixture", Revision: "content_a"},
	}
	change := intent.Change{ID: "change_one"}
	version := intent.Version{
		ID:           "version_one",
		ChangeID:     change.ID,
		BaseIntent:   initial.ID,
		Content:      intent.ContentRef{Engine: "fixture", Revision: "content_b"},
		Producer:     "principal:author",
		Dependencies: []intent.VersionID{},
	}
	evaluation := intent.Evaluation{
		VersionID:       version.ID,
		GoverningIntent: initial.ID,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:      "architecture",
			Instruction: "Check the architecture boundary.",
			Assignee:    "principal:architect",
			Provenance: intent.EvaluatorProvenance{
				Evaluator:        "fixture-evaluator",
				ContractRevision: "fixture/v1",
			},
			RequiresAction: true,
			Reason:         "the change crosses a boundary",
			Evidence:       []string{"internal/example.go"},
		}},
	}
	response := intent.RequirementResponse{
		ID:        "response_one",
		VersionID: version.ID,
		Policy:    "architecture",
		Assignee:  "principal:architect",
		Decision:  intent.RequirementSatisfied,
		Rationale: "the boundary is intentional",
	}
	prepared := intent.PreparedPromotion{
		Promotion: intent.Promotion{
			ID:         "promotion_one",
			FromIntent: initial.ID,
			ToIntent:   "intent_next",
			VersionID:  version.ID,
		},
		Intent: intent.Revision{
			ID:         "intent_next",
			PreviousID: initial.ID,
			Content:    version.Content,
		},
	}

	steps := []struct {
		name string
		run  func() error
	}{
		{"initialize", func() error { return ledger.Initialize(ctx, initial) }},
		{"proposal", func() error { return ledger.RecordProposal(ctx, "proposal-key", change, version) }},
		{"evaluation", func() error { return ledger.RecordEvaluation(ctx, evaluation) }},
		{"response", func() error { return ledger.RecordRequirementResponse(ctx, "response-key", response) }},
		{"prepare promotion", func() error { return ledger.PreparePromotion(ctx, prepared) }},
		{"complete promotion", func() error { return ledger.CompletePromotion(ctx, prepared.Promotion.ID) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "journal_v1_decision_loop.jsonl"))
	if err != nil {
		t.Fatalf("read v1 fixture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("journal v1 changed\ngot:\n%s\nwant:\n%s", got, want)
	}

	replayPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := os.WriteFile(replayPath, want, 0o600); err != nil {
		t.Fatalf("copy v1 fixture: %v", err)
	}
	replayed, err := ledgerfs.Open(replayPath)
	if err != nil {
		t.Fatalf("replay v1 fixture: %v", err)
	}
	t.Cleanup(func() { _ = replayed.Close() })
	current, found, err := replayed.CurrentIntent(ctx)
	if err != nil || !found || current != prepared.Intent {
		t.Fatalf("replayed current Intent = %#v, %t, %v; want %#v, true, nil", current, found, err, prepared.Intent)
	}
	promoted, found, err := replayed.CompletedPromotion(ctx, version.ID)
	if err != nil || !found || promoted.Promotion != prepared.Promotion || promoted.Intent != prepared.Intent {
		t.Fatalf("replayed promotion = %#v, %t, %v; want %#v, true, nil", promoted, found, err, prepared)
	}
	replayedEvaluation, found, err := replayed.Evaluation(ctx, version.ID)
	if err != nil || !found || len(replayedEvaluation.PolicyEvaluations) != 1 ||
		replayedEvaluation.PolicyEvaluations[0].Policy != evaluation.PolicyEvaluations[0].Policy {
		t.Fatalf("replayed evaluation = %#v, %t, %v", replayedEvaluation, found, err)
	}
	responses, err := replayed.RequirementResponses(ctx, version.ID)
	if err != nil || len(responses) != 1 || responses[0] != response {
		t.Fatalf("replayed responses = %#v, %v; want %#v, nil", responses, err, []intent.RequirementResponse{response})
	}
}
