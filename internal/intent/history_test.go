package intent_test

import (
	"context"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
)

func TestHistoryPagesImmutableRepositoryFactsInRecordedOrder(t *testing.T) {
	ctx := context.Background()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initial, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "proposal-one",
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "local:ion",
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	_, err = repository.RecordEvaluation(ctx, intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:         "architecture",
			Instruction:    "Review the boundary.",
			Assignee:       "local:ion",
			RequiresAction: true,
			Reason:         "The boundary changed.",
			Evidence:       []string{"store.go"},
			Provenance:     intent.EvaluatorProvenance{Evaluator: "test", ContractRevision: "v1"},
		}},
	})
	if err != nil {
		t.Fatalf("record Evaluation: %v", err)
	}
	_, err = repository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "response-one",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture",
		Assignee:       "local:ion",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "Boundary reviewed.",
	})
	if err != nil {
		t.Fatalf("record Response: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: proposed.Version.BaseIntent}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	first, err := repository.History(ctx, intent.HistoryQuery{Limit: 3})
	if err != nil {
		t.Fatalf("first history page: %v", err)
	}
	if len(first.Facts) != 3 || first.NextCursor == 0 {
		t.Fatalf("first history page = %#v", first)
	}
	if first.Facts[0].Kind != intent.HistoryIntentInitialized || first.Facts[1].Kind != intent.HistoryVersionProposed || first.Facts[2].Kind != intent.HistoryEvaluationRecorded {
		t.Fatalf("first history facts = %#v", first.Facts)
	}
	second, err := repository.History(ctx, intent.HistoryQuery{After: first.NextCursor, Limit: 3})
	if err != nil {
		t.Fatalf("second history page: %v", err)
	}
	if len(second.Facts) != 2 || second.NextCursor != 0 || second.Facts[0].Kind != intent.HistoryRequirementResponded || second.Facts[1].Kind != intent.HistoryVersionPromoted {
		t.Fatalf("second history page = %#v", second)
	}
}
