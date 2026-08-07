package intent_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sky-valley/grd/internal/intent"
)

func TestRepositoryRecordsExactVersionEvaluationAndDerivesRequirements(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "bbbbbbbb")

	evaluation := intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{
			{
				Policy:         "architecture-and-data",
				Instruction:    "Does this change modify architecture or data models?",
				Assignee:       "principal:architecture",
				RequiresAction: true,
				Reason:         "the candidate adds a database-backed account model",
				Evidence:       []string{"internal/account/model.go", "migrations/001_accounts.sql"},
			},
			{
				Policy:      "copy-and-commercial-impact",
				Instruction: "Does this change modify copy or commercial behavior?",
				Assignee:    "principal:commercial",
				Reason:      "no user-facing or commercial language changed",
				Evidence:    []string{"only internal account storage changed"},
			},
		},
	}
	recorded, err := repository.RecordEvaluation(ctx, evaluation)
	if err != nil {
		t.Fatalf("record evaluation: %v", err)
	}
	if !reflect.DeepEqual(recorded, evaluation) {
		t.Fatalf("recorded evaluation = %#v, want %#v", recorded, evaluation)
	}

	got, found, err := repository.Evaluation(ctx, proposed.Version.ID)
	if err != nil || !found || !reflect.DeepEqual(got, evaluation) {
		t.Fatalf("read evaluation = %#v, %t, %v; want %#v, true, nil", got, found, err, evaluation)
	}
	wantRequirements := []intent.Requirement{{
		VersionID: proposed.Version.ID,
		Policy:    "architecture-and-data",
		Assignee:  "principal:architecture",
		Reason:    "the candidate adds a database-backed account model",
		Evidence:  []string{"internal/account/model.go", "migrations/001_accounts.sql"},
	}}
	if got := evaluation.Requirements(); !reflect.DeepEqual(got, wantRequirements) {
		t.Fatalf("requirements = %#v, want %#v", got, wantRequirements)
	}

	pending, err := repository.PendingEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list pending evaluation: %v", err)
	}
	if len(pending.Versions) != 1 || pending.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("pending evaluation = %#v, want held Version %q", pending, proposed.Version.ID)
	}
	runnable, err := repository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable evaluation: %v", err)
	}
	if len(runnable.Versions) != 0 {
		t.Fatalf("runnable evaluation = %#v, want held Version excluded", runnable)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: proposed.Version.BaseIntent,
	}); !errors.Is(err, intent.ErrRequirementRequired) {
		t.Fatalf("direct promotion while requirement is required error = %v, want ErrRequirementRequired", err)
	}
}

func TestRepositoryKeepsClearEvaluationRunnableUntilPromotion(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "cccccccc")

	_, err := repository.RecordEvaluation(ctx, intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:      "architecture-and-data",
			Instruction: "Does this change modify architecture or data models?",
			Assignee:    "principal:architecture",
			Reason:      "no architecture, data-model, or infrastructure change",
			Evidence:    []string{"README.md typo only"},
		}},
	})
	if err != nil {
		t.Fatalf("record clear evaluation: %v", err)
	}
	runnable, err := repository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable evaluation: %v", err)
	}
	if len(runnable.Versions) != 1 || runnable.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("runnable evaluation = %#v, want clear Version %q", runnable, proposed.Version.ID)
	}

	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: proposed.Version.BaseIntent,
	}); err != nil {
		t.Fatalf("promote clear Version: %v", err)
	}
	runnable, err = repository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable evaluation after promotion: %v", err)
	}
	if len(runnable.Versions) != 0 {
		t.Fatalf("runnable evaluation after promotion = %#v, want none", runnable)
	}
}

func TestRepositoryDoesNotCarryEvaluationAcrossVersionReplacement(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "dddddddd")
	first := intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:         "design-and-user-experience",
			Instruction:    "Does this change modify design or user experience?",
			Assignee:       "principal:contributor",
			RequiresAction: true,
			Reason:         "the candidate changes the primary navigation",
			Evidence:       []string{"ui/navigation.go"},
		}},
	}
	if _, err := repository.RecordEvaluation(ctx, first); err != nil {
		t.Fatalf("record first evaluation: %v", err)
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  "amend-after-requirement",
		ChangeID:        proposed.Change.ID,
		ExpectedVersion: proposed.Version.ID,
		Content:         intent.ContentRef{Engine: "git", Revision: "eeeeeeee"},
		Producer:        "repository-agent",
		Rationale:       "restore the existing navigation",
	})
	if err != nil {
		t.Fatalf("amend evaluated Version: %v", err)
	}

	if got, found, err := repository.Evaluation(ctx, proposed.Version.ID); err != nil || !found || !reflect.DeepEqual(got, first) {
		t.Fatalf("historical evaluation = %#v, %t, %v; want %#v, true, nil", got, found, err, first)
	}
	if _, found, err := repository.Evaluation(ctx, amended.Version.ID); err != nil || found {
		t.Fatalf("replacement evaluation found = %t, error = %v; want false, nil", found, err)
	}
	runnable, err := repository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list replacement evaluation: %v", err)
	}
	if len(runnable.Versions) != 1 || runnable.Versions[0].ID != amended.Version.ID {
		t.Fatalf("runnable replacement = %#v, want unevaluated Version %q", runnable, amended.Version.ID)
	}

	retried, err := repository.RecordEvaluation(ctx, intent.Evaluation{
		VersionID:         proposed.Version.ID,
		GoverningIntent:   proposed.Version.BaseIntent,
		PolicyEvaluations: first.PolicyEvaluations,
	})
	if err != nil || !reflect.DeepEqual(retried, first) {
		t.Fatalf("retry historical evaluation = %#v, %v; want %#v, nil", retried, err, first)
	}
	conflicting := first
	conflicting.PolicyEvaluations = append([]intent.PolicyEvaluation(nil), first.PolicyEvaluations...)
	conflicting.PolicyEvaluations[0].Reason = "a different result arrived later"
	if _, err := repository.RecordEvaluation(ctx, conflicting); !errors.Is(err, intent.ErrEvaluationAlreadyRecorded) {
		t.Fatalf("replace historical evaluation error = %v, want ErrEvaluationAlreadyRecorded", err)
	}
}

func TestRepositoryRejectsNoncanonicalPolicyIdentity(t *testing.T) {
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "abababab")
	_, err := repository.RecordEvaluation(context.Background(), intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:      " architecture ",
			Instruction: "Does this change modify architecture?",
			Assignee:    "principal:architecture",
			Reason:      "architecture changed",
			Evidence:    []string{"internal/model.go"},
		}},
	})
	if err == nil {
		t.Fatal("record evaluation with noncanonical policy identity succeeded, want error")
	}
}

func TestRepositoryRejectsPartialEvaluatorProvenance(t *testing.T) {
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "acacacac")
	_, err := repository.RecordEvaluation(context.Background(), intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:      "architecture",
			Instruction: "Does this change modify architecture?",
			Assignee:    "principal:architecture",
			Provenance:  intent.EvaluatorProvenance{Evaluator: "example://deterministic"},
			Reason:      "architecture changed",
			Evidence:    []string{"internal/model.go"},
		}},
	})
	if err == nil {
		t.Fatal("record evaluation with partial evaluator provenance succeeded, want error")
	}
}

func TestRepositoryRecordsAssignedPrincipalSatisfactionAndReleasesExactVersion(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "cdcdcdcd")
	recordBlockingArchitectureEvaluation(t, repository, proposed)

	satisfied, err := repository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "satisfy-architecture",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture-and-data",
		Assignee:       "principal:architecture",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "the migration and rollback plan are acceptable",
	})
	if err != nil {
		t.Fatalf("record satisfaction: %v", err)
	}
	if satisfied.ID == "" || satisfied.VersionID != proposed.Version.ID || satisfied.Decision != intent.RequirementSatisfied {
		t.Fatalf("satisfaction = %#v", satisfied)
	}
	retried, err := repository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "satisfy-architecture",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture-and-data",
		Assignee:       "principal:architecture",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "the migration and rollback plan are acceptable",
	})
	if err != nil || !reflect.DeepEqual(retried, satisfied) {
		t.Fatalf("retry satisfaction = %#v, %v; want %#v, nil", retried, err, satisfied)
	}

	open, err := repository.PendingRequirements(ctx, intent.PendingRequirementQuery{Assignee: "principal:architecture", Limit: 10})
	if err != nil {
		t.Fatalf("list open requirements: %v", err)
	}
	if len(open.Requirements) != 0 {
		t.Fatalf("open requirements after satisfaction = %#v, want none", open)
	}
	runnable, err := repository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable evaluations: %v", err)
	}
	if len(runnable.Versions) != 1 || runnable.Versions[0].ID != proposed.Version.ID {
		t.Fatalf("runnable evaluations = %#v, want satisfied Version", runnable)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: proposed.Version.BaseIntent}); err != nil {
		t.Fatalf("promote satisfied Version: %v", err)
	}
}

func TestRepositoryEnforcesAssignedAssigneeAndKeepsRevisionRequestedHeld(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "dededede")
	recordBlockingArchitectureEvaluation(t, repository, proposed)

	_, err := repository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "wrong-assignee",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture-and-data",
		Assignee:       "principal:contributor",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "looks fine",
	})
	if !errors.Is(err, intent.ErrRequirementNotAssigned) {
		t.Fatalf("wrong assignee error = %v, want ErrRequirementNotAssigned", err)
	}
	response, err := repository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "request-architecture-changes",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture-and-data",
		Assignee:       "principal:architecture",
		Decision:       intent.RequirementRevisionRequested,
		Rationale:      "add a rollback path before this becomes intent",
	})
	if err != nil {
		t.Fatalf("request changes: %v", err)
	}
	if response.Decision != intent.RequirementRevisionRequested {
		t.Fatalf("decision = %q", response.Decision)
	}
	open, err := repository.PendingRequirements(ctx, intent.PendingRequirementQuery{Assignee: "principal:architecture", Limit: 10})
	if err != nil {
		t.Fatalf("list open requirements: %v", err)
	}
	if len(open.Requirements) != 1 || open.Requirements[0].Policy != "architecture-and-data" || open.Requirements[0].LatestResponse == nil {
		t.Fatalf("open requirements = %#v, want revision-requested requirement", open)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: proposed.Version.BaseIntent}); !errors.Is(err, intent.ErrRequirementRequired) {
		t.Fatalf("promotion after changes requested error = %v, want ErrRequirementRequired", err)
	}
	if _, err := repository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "satisfy-after-revision-requested",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture-and-data",
		Assignee:       "principal:architecture",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "rollback path is now documented",
	}); err != nil {
		t.Fatalf("satisfy after changes requested: %v", err)
	}
	open, err = repository.PendingRequirements(ctx, intent.PendingRequirementQuery{Assignee: "principal:architecture", Limit: 10})
	if err != nil || len(open.Requirements) != 0 {
		t.Fatalf("open requirements after later satisfaction = %#v, %v; want none", open, err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: proposed.Version.ID, ExpectedIntent: proposed.Version.BaseIntent}); err != nil {
		t.Fatalf("promote after later satisfaction: %v", err)
	}
}

func TestRepositoryAcceptsOpaquePrincipalAsAssignedAssignee(t *testing.T) {
	repository := newTestRepository(t)
	ctx := context.Background()
	proposed := proposeForEvaluation(t, repository, "abababab")

	_, err := repository.RecordEvaluation(ctx, intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:         "architecture",
			Instruction:    "Does this alter architecture?",
			Assignee:       "github:User:12345",
			RequiresAction: true,
			Reason:         "architecture changed",
			Evidence:       []string{"architecture.go"},
			Provenance: intent.EvaluatorProvenance{
				Evaluator:        "test://evaluator",
				ContractRevision: "test/v1",
			},
		}},
	})
	if err != nil {
		t.Fatalf("record evaluation for opaque principal: %v", err)
	}
}

func TestPendingRequirementCursorDistinguishesPoliciesOnOneVersion(t *testing.T) {
	ctx := context.Background()
	repository := newTestRepository(t)
	proposed := proposeForEvaluation(t, repository, "efefefef")
	_, err := repository.RecordEvaluation(ctx, intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{
			{Policy: "architecture", Instruction: "Architecture change?", Assignee: "principal:architecture", RequiresAction: true, Reason: "architecture changed", Evidence: []string{"architecture.go"}},
			{Policy: "infrastructure", Instruction: "Infrastructure change?", Assignee: "principal:architecture", RequiresAction: true, Reason: "infrastructure changed", Evidence: []string{"deploy.go"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.PendingRequirements(ctx, intent.PendingRequirementQuery{Assignee: "principal:architecture", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Requirements) != 1 || first.Requirements[0].Policy != "architecture" || first.NextCursor.Policy != "architecture" {
		t.Fatalf("first page = %#v", first)
	}
	if _, err := repository.RecordRequirementResponse(ctx, intent.RequirementResponseRequest{
		IdempotencyKey: "satisfy-first-page",
		VersionID:      proposed.Version.ID,
		Policy:         "architecture",
		Assignee:       "principal:architecture",
		Decision:       intent.RequirementSatisfied,
		Rationale:      "architecture evaluated",
	}); err != nil {
		t.Fatal(err)
	}
	second, err := repository.PendingRequirements(ctx, intent.PendingRequirementQuery{Assignee: "principal:architecture", After: first.NextCursor, Limit: 1})
	if err != nil {
		t.Fatalf("page after resolved cursor: %v", err)
	}
	if len(second.Requirements) != 1 || second.Requirements[0].Policy != "infrastructure" || second.NextCursor.VersionID != "" {
		t.Fatalf("second page = %#v", second)
	}
}

func recordBlockingArchitectureEvaluation(t *testing.T, repository *intent.Repository, proposed intent.Proposed) {
	t.Helper()
	_, err := repository.RecordEvaluation(context.Background(), intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: proposed.Version.BaseIntent,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:         "architecture-and-data",
			Instruction:    "Does this change modify architecture or data models?",
			Assignee:       "principal:architecture",
			RequiresAction: true,
			Reason:         "the candidate adds a database-backed account model",
			Evidence:       []string{"internal/account/model.go"},
		}},
	})
	if err != nil {
		t.Fatalf("record blocking evaluation: %v", err)
	}
}

func newTestRepository(t *testing.T) *intent.Repository {
	t.Helper()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	repository, err := intent.NewEphemeralRepository(initial, &recordingAdmission{}, &recordingProjection{current: initial})
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return repository
}

func proposeForEvaluation(t *testing.T, repository *intent.Repository, revision string) intent.Proposed {
	t.Helper()
	proposed, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "propose-" + revision,
		BaseIntent:     repository.CurrentIntent().ID,
		Content:        intent.ContentRef{Engine: "git", Revision: revision},
		Producer:       "principal:contributor",
	})
	if err != nil {
		t.Fatalf("propose %s: %v", revision, err)
	}
	return proposed
}
