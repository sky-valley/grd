package intent_test

import (
	"context"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
)

func TestRepositoryRecordsGenericEvaluationAndDerivesRequirement(t *testing.T) {
	if got := string(intent.RequirementSatisfied); got != "satisfied" {
		t.Fatalf("satisfied decision = %q", got)
	}
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "acacacac")
	ctx := context.Background()

	_, err := repository.RecordEvaluation(ctx, intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:         "architecture",
			Instruction:    "Determine whether architecture authority is required.",
			Assignee:       "github:User:12345",
			RequiresAction: true,
			Reason:         "architecture changed",
			Evidence:       []string{"internal/store.go"},
			Provenance: intent.EvaluatorProvenance{
				Evaluator:        "example://deterministic",
				ContractRevision: "example/v1",
			},
		}},
	})
	if err != nil {
		t.Fatalf("record evaluation: %v", err)
	}

	page, err := repository.PendingRequirements(ctx, intent.PendingRequirementQuery{
		Assignee: "github:User:12345",
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("pending requirements: %v", err)
	}
	if len(page.Requirements) != 1 || page.Requirements[0].Policy != "architecture" {
		t.Fatalf("requirements = %#v", page.Requirements)
	}

	_, err = repository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "satisfy-architecture",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture",
		Assignee:       "github:User:12345",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "architecture authority accepted the change",
	})
	if err != nil {
		t.Fatalf("satisfy requirement: %v", err)
	}
}
