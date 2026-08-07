package evaluation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sky-valley/grd/internal/evaluation"
	"github.com/sky-valley/grd/internal/intent"
)

var testEvaluatorProvenance = intent.EvaluatorProvenance{
	Evaluator:        "test://policy-evaluator",
	ContractRevision: "test.policy-evaluation/v1",
}

func TestPolicyProcessorRecordsAllMatchingAssignedRequirementsAgainstExactVersion(t *testing.T) {
	version, governing := evaluationFixture()
	service := &recordingPolicyService{context: intent.EvaluationContext{Version: version, GoverningIntent: governing}}
	evaluator := &recordingEvaluator{results: map[string]evaluation.EvaluationResult{
		"architecture-data-infrastructure": {
			RequiresAction: true,
			Reason:         "adds a persistent reservation model and DATABASE_URL",
			Evidence:       []string{"internal/reservation/model.go", "cmd/app/config.go"},
		},
		"design-system-user-experience": {
			RequiresAction: true,
			Reason:         "changes the booking form interaction",
			Evidence:       []string{"web/booking-form.tsx"},
		},
		"copy-commercial-impact": {
			Reason:   "no copy or commercial behavior changed",
			Evidence: []string{"no customer-facing text in the candidate diff"},
		},
		"prompts-models": {
			Reason:   "no prompt, model, or LLM use changed",
			Evidence: []string{"no model integration files in the candidate diff"},
		},
	}}
	processor := newFourPolicyProcessor(t, service, evaluator)

	if err := processor.Process(context.Background(), evaluation.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("process Version: %v", err)
	}
	if len(evaluator.requests) != 4 {
		t.Fatalf("evaluation requests = %d, want 4", len(evaluator.requests))
	}
	for _, request := range evaluator.requests {
		if request.RepoID != "repo_app" || !reflect.DeepEqual(request.Version, version) || request.GoverningIntent != governing {
			t.Fatalf("evaluation request = %#v, want exact repo, Version, and governing Intent", request)
		}
		if request.Purpose != "test purpose" || request.Priorities != "test priorities" || request.ChangeEvidence != "test change evidence" {
			t.Fatalf("evaluation repository inputs = %#v", request)
		}
	}
	want := intent.Evaluation{
		VersionID:       version.ID,
		GoverningIntent: governing.ID,
		PolicyEvaluations: []intent.PolicyEvaluation{
			{
				Policy:         "architecture-data-infrastructure",
				Instruction:    "Does this change modify architecture, data models, or infrastructure requirements?",
				Assignee:       "principal:architecture",
				Provenance:     testEvaluatorProvenance,
				RequiresAction: true,
				Reason:         "adds a persistent reservation model and DATABASE_URL",
				Evidence:       []string{"internal/reservation/model.go", "cmd/app/config.go"},
			},
			{
				Policy:         "design-system-user-experience",
				Instruction:    "Does this change modify the design system or user experience?",
				Assignee:       "principal:contributor",
				Provenance:     testEvaluatorProvenance,
				RequiresAction: true,
				Reason:         "changes the booking form interaction",
				Evidence:       []string{"web/booking-form.tsx"},
			},
			{
				Policy:      "copy-commercial-impact",
				Instruction: "Does this change modify copywriting or commercial behavior?",
				Assignee:    "principal:commercial",
				Provenance:  testEvaluatorProvenance,
				Reason:      "no copy or commercial behavior changed",
				Evidence:    []string{"no customer-facing text in the candidate diff"},
			},
			{
				Policy:      "prompts-models",
				Instruction: "Does this change modify prompts, LLM usage, or model selection?",
				Assignee:    "principal:models",
				Provenance:  testEvaluatorProvenance,
				Reason:      "no prompt, model, or LLM use changed",
				Evidence:    []string{"no model integration files in the candidate diff"},
			},
		},
	}
	if !reflect.DeepEqual(service.recorded, want) {
		t.Fatalf("recorded evaluation = %#v, want %#v", service.recorded, want)
	}
	if len(service.promotions) != 0 {
		t.Fatalf("promotions = %#v, want none while assigned requirements are required", service.promotions)
	}
	requirements := service.recorded.Requirements()
	if len(requirements) != 2 || requirements[0].Assignee != "principal:architecture" || requirements[1].Assignee != "principal:contributor" {
		t.Fatalf("requirements = %#v, want Maintainer and Contributor", requirements)
	}
}

func TestPolicyProcessorPromotesOnlyAfterDurableClearEvaluation(t *testing.T) {
	version, governing := evaluationFixture()
	service := &recordingPolicyService{context: intent.EvaluationContext{Version: version, GoverningIntent: governing}}
	evaluator := &recordingEvaluator{defaultResult: evaluation.EvaluationResult{
		Reason:   "the policy does not apply",
		Evidence: []string{"candidate diff contains no matching semantic change"},
	}}
	processor := newFourPolicyProcessor(t, service, evaluator)

	if err := processor.Process(context.Background(), evaluation.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("process Version: %v", err)
	}
	if service.recordSequence != 1 || service.promoteSequence != 2 {
		t.Fatalf("effect order = record %d, promote %d; want record 1, promote 2", service.recordSequence, service.promoteSequence)
	}
	if len(service.promotions) != 1 || service.promotions[0] != (intent.PromoteRequest{VersionID: version.ID, ExpectedIntent: governing.ID}) {
		t.Fatalf("promotions = %#v, want exact governed Version promotion", service.promotions)
	}
}

func TestPolicyProcessorResumesPersistedEvaluationWithoutReevaluating(t *testing.T) {
	version, governing := evaluationFixture()
	existing := intent.Evaluation{
		VersionID:       version.ID,
		GoverningIntent: governing.ID,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:      "architecture-data-infrastructure",
			Instruction: "Does this change modify architecture, data models, or infrastructure requirements?",
			Assignee:    "principal:architecture",
			Reason:      "the policy does not apply",
			Evidence:    []string{"README-only change"},
		}},
	}
	service := &recordingPolicyService{existing: existing, existingFound: true}
	evaluator := &recordingEvaluator{err: errors.New("must not be called")}
	processor := newFourPolicyProcessor(t, service, evaluator)

	if err := processor.Process(context.Background(), evaluation.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("resume persisted evaluation: %v", err)
	}
	if len(evaluator.requests) != 0 || service.recorded.VersionID != "" {
		t.Fatalf("resume reevaluated or rerecorded: requests %#v, recorded %#v", evaluator.requests, service.recorded)
	}
	if len(service.promotions) != 1 || service.promotions[0].ExpectedIntent != governing.ID {
		t.Fatalf("resumed promotions = %#v, want persisted governing Intent", service.promotions)
	}
}

func TestPolicyProcessorLeavesVersionUnevaluatedWhenAnArmFails(t *testing.T) {
	version, governing := evaluationFixture()
	service := &recordingPolicyService{context: intent.EvaluationContext{Version: version, GoverningIntent: governing}}
	evaluator := &recordingEvaluator{err: errors.New("model unavailable")}
	processor := newFourPolicyProcessor(t, service, evaluator)

	err := processor.Process(context.Background(), evaluation.WorkItem{RepoID: "repo_app", VersionID: version.ID})
	if err == nil || !errors.Is(err, evaluator.err) {
		t.Fatalf("process error = %v, want model failure", err)
	}
	if service.recorded.VersionID != "" || len(service.promotions) != 0 {
		t.Fatalf("failed evaluation recorded or promoted: %#v, %#v", service.recorded, service.promotions)
	}
}

func TestPolicyProcessorIsolatesExactVersionInputBetweenEvaluatorArms(t *testing.T) {
	version, governing := evaluationFixture()
	version.Dependencies = []intent.VersionID{"version_parent"}
	wantDependencies := append([]intent.VersionID(nil), version.Dependencies...)
	service := &recordingPolicyService{context: intent.EvaluationContext{Version: version, GoverningIntent: governing}}
	evaluator := &mutatingEvaluator{}
	processor := newPolicyProcessor(t, service, evaluator, []evaluation.Policy{
		{Name: "first", Instruction: "Does the first policy apply?", Assignee: "first@example.com"},
		{Name: "second", Instruction: "Does the second policy apply?", Assignee: "second@example.com"},
	})

	if err := processor.Process(context.Background(), evaluation.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("process Version: %v", err)
	}
	if !reflect.DeepEqual(evaluator.secondDependencies, wantDependencies) {
		t.Fatalf("second arm dependencies = %q, want exact original %q", evaluator.secondDependencies, wantDependencies)
	}
}

func TestPolicyProcessorTrimsAndPreservesOpaqueAssigneeAuthority(t *testing.T) {
	version, governing := evaluationFixture()
	service := &recordingPolicyService{context: intent.EvaluationContext{Version: version, GoverningIntent: governing}}
	processor := newPolicyProcessor(t, service, &recordingEvaluator{defaultResult: evaluation.EvaluationResult{
		RequiresAction: true,
		Reason:         "the candidate changes architecture",
		Evidence:       []string{"internal/model.go"},
	}}, []evaluation.Policy{{
		Name:        "architecture",
		Instruction: "Does this change modify architecture?",
		Assignee:    " github:User:12345 ",
	}})
	if err := processor.Process(context.Background(), evaluation.WorkItem{RepoID: "repo_app", VersionID: version.ID}); err != nil {
		t.Fatalf("process Version: %v", err)
	}
	requirements := service.recorded.Requirements()
	if len(requirements) != 1 || requirements[0].Assignee != "github:User:12345" {
		t.Fatalf("requirements = %#v, want canonical assignee authority", requirements)
	}
}

func TestPolicyProcessorDoesNotTreatOldEvaluationAsAuthorityAfterParentAdvancesIntent(t *testing.T) {
	ctx := context.Background()
	initial := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	projection := &processorProjection{current: initial}
	repository, err := intent.NewEphemeralRepository(initial, acceptingContent{}, projection)
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	base := repository.CurrentIntent()
	parent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "dependent-parent",
		BaseIntent:     base.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:       "principal:contributor",
	})
	if err != nil {
		t.Fatalf("propose parent: %v", err)
	}
	dependent, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: "dependent-child",
		BaseIntent:     base.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: "cccccccc"},
		Producer:       "principal:contributor",
		Dependencies:   []intent.VersionID{parent.Version.ID},
	})
	if err != nil {
		t.Fatalf("propose dependent: %v", err)
	}
	service := &repositoryPolicyService{repository: repository}
	processor := newPolicyProcessor(t, service, &recordingEvaluator{defaultResult: evaluation.EvaluationResult{
		Reason:   "the policy does not apply",
		Evidence: []string{"no matching semantic change"},
	}}, []evaluation.Policy{{
		Name:        "architecture-data-infrastructure",
		Instruction: "Does this change modify architecture, data models, or infrastructure requirements?",
		Assignee:    "principal:architecture",
	}})
	item := evaluation.WorkItem{RepoID: "repo_app", VersionID: dependent.Version.ID}
	if err := processor.Process(ctx, item); err != nil {
		t.Fatalf("assess dependent before parent promotion: %v", err)
	}
	if _, found, err := repository.Evaluation(ctx, dependent.Version.ID); err != nil || !found {
		t.Fatalf("dependent evaluation found = %t, error = %v; want true, nil", found, err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{VersionID: parent.Version.ID, ExpectedIntent: base.ID}); err != nil {
		t.Fatalf("promote parent: %v", err)
	}
	if _, err := repository.Promote(ctx, intent.PromoteRequest{
		VersionID:      dependent.Version.ID,
		ExpectedIntent: repository.CurrentIntent().ID,
	}); !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("direct promotion with stale evaluation error = %v, want ErrIntentAdvanced", err)
	}
	if err := processor.Process(ctx, item); err != nil {
		t.Fatalf("reconsider dependent after parent promotion: %v", err)
	}
	if got := repository.CurrentIntent().Content; got != parent.Version.Content {
		t.Fatalf("current content = %#v, want parent content %#v until re-triage", got, parent.Version.Content)
	}
	runnable, err := repository.RunnableEvaluations(ctx, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list runnable evaluations: %v", err)
	}
	if len(runnable.Versions) != 0 {
		t.Fatalf("runnable evaluations = %#v, want old-governance evaluation deferred", runnable)
	}
}

func newFourPolicyProcessor(t *testing.T, service evaluation.PolicyService, evaluator evaluation.Evaluator) *evaluation.PolicyProcessor {
	t.Helper()
	return newPolicyProcessor(t, service, evaluator, []evaluation.Policy{
		{Name: "architecture-data-infrastructure", Instruction: "Does this change modify architecture, data models, or infrastructure requirements?", Assignee: "principal:architecture"},
		{Name: "design-system-user-experience", Instruction: "Does this change modify the design system or user experience?", Assignee: "principal:contributor"},
		{Name: "copy-commercial-impact", Instruction: "Does this change modify copywriting or commercial behavior?", Assignee: "principal:commercial"},
		{Name: "prompts-models", Instruction: "Does this change modify prompts, LLM usage, or model selection?", Assignee: "principal:models"},
	})

}

func newPolicyProcessor(t *testing.T, service evaluation.PolicyService, evaluator evaluation.Evaluator, policies []evaluation.Policy) *evaluation.PolicyProcessor {
	t.Helper()
	processor, err := evaluation.NewPolicyProcessor(service, evaluator, staticEvaluationInputSource{input: evaluation.EvaluationInput{
		Purpose:        "test purpose",
		Priorities:     "test priorities",
		ChangeEvidence: "test change evidence",
		Policies:       policies,
	}})
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}
	return processor
}

type staticEvaluationInputSource struct {
	input evaluation.EvaluationInput
	err   error
}

func (source staticEvaluationInputSource) Load(context.Context, string, intent.EvaluationContext) (evaluation.EvaluationInput, error) {
	return source.input, source.err
}

func evaluationFixture() (intent.Version, intent.Revision) {
	governing := intent.Revision{
		ID:      "intent_a",
		Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"},
	}
	version := intent.Version{
		ID:         "version_b",
		ChangeID:   "change_b",
		BaseIntent: governing.ID,
		Content:    intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"},
		Producer:   "principal:contributor",
	}
	return version, governing
}

type recordingPolicyService struct {
	context         intent.EvaluationContext
	existing        intent.Evaluation
	existingFound   bool
	recorded        intent.Evaluation
	promotions      []intent.PromoteRequest
	sequence        int
	recordSequence  int
	promoteSequence int
}

type repositoryPolicyService struct {
	repository *intent.Repository
}

func (service *repositoryPolicyService) Evaluation(ctx context.Context, _ string, versionID intent.VersionID) (intent.Evaluation, bool, error) {
	return service.repository.Evaluation(ctx, versionID)
}

func (service *repositoryPolicyService) EvaluationContext(ctx context.Context, _ string, versionID intent.VersionID) (intent.EvaluationContext, error) {
	return service.repository.EvaluationContext(ctx, versionID)
}

func (service *repositoryPolicyService) RecordEvaluation(ctx context.Context, _ string, evaluation intent.Evaluation) (intent.Evaluation, error) {
	return service.repository.RecordEvaluation(ctx, evaluation)
}

func (service *repositoryPolicyService) UnresolvedRequirements(ctx context.Context, _ string, versionID intent.VersionID) ([]intent.Requirement, error) {
	return service.repository.UnresolvedRequirements(ctx, versionID)
}

func (service *repositoryPolicyService) Promote(ctx context.Context, _ string, request intent.PromoteRequest) (intent.Promoted, error) {
	return service.repository.Promote(ctx, request)
}

type acceptingContent struct{}

func (acceptingContent) Admit(context.Context, intent.VersionID, intent.ContentRef) error { return nil }

type processorProjection struct {
	current intent.ContentRef
}

func (projection *processorProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *processorProjection) Advance(_ context.Context, expected, next intent.ContentRef) error {
	if projection.current != expected {
		return errors.New("projection advanced")
	}
	projection.current = next
	return nil
}

func (service *recordingPolicyService) Evaluation(context.Context, string, intent.VersionID) (intent.Evaluation, bool, error) {
	return service.existing, service.existingFound, nil
}

func (service *recordingPolicyService) EvaluationContext(context.Context, string, intent.VersionID) (intent.EvaluationContext, error) {
	return service.context, nil
}

func (service *recordingPolicyService) RecordEvaluation(_ context.Context, _ string, recorded intent.Evaluation) (intent.Evaluation, error) {
	service.sequence++
	service.recordSequence = service.sequence
	service.recorded = recorded
	service.existing = recorded
	service.existingFound = true
	return recorded, nil
}

func (service *recordingPolicyService) UnresolvedRequirements(context.Context, string, intent.VersionID) ([]intent.Requirement, error) {
	return service.existing.Requirements(), nil
}

func (service *recordingPolicyService) Promote(_ context.Context, _ string, request intent.PromoteRequest) (intent.Promoted, error) {
	service.sequence++
	service.promoteSequence = service.sequence
	service.promotions = append(service.promotions, request)
	return intent.Promoted{}, nil
}

type recordingEvaluator struct {
	results       map[string]evaluation.EvaluationResult
	defaultResult evaluation.EvaluationResult
	requests      []evaluation.EvaluationRequest
	err           error
}

type mutatingEvaluator struct {
	calls              int
	secondDependencies []intent.VersionID
}

func (evaluator *mutatingEvaluator) Evaluate(_ context.Context, request evaluation.EvaluationRequest) (evaluation.EvaluationResult, error) {
	evaluator.calls++
	if evaluator.calls == 1 {
		request.Version.Dependencies[0] = "version_mutated"
	} else {
		evaluator.secondDependencies = append([]intent.VersionID(nil), request.Version.Dependencies...)
	}
	return evaluation.EvaluationResult{Reason: "the policy does not apply", Evidence: []string{"no matching change"}, Provenance: testEvaluatorProvenance}, nil
}

func (evaluator *recordingEvaluator) Evaluate(_ context.Context, request evaluation.EvaluationRequest) (evaluation.EvaluationResult, error) {
	evaluator.requests = append(evaluator.requests, request)
	if evaluator.err != nil {
		return evaluation.EvaluationResult{}, evaluator.err
	}
	if result, found := evaluator.results[request.Policy.Name]; found {
		if result.Provenance == (intent.EvaluatorProvenance{}) {
			result.Provenance = testEvaluatorProvenance
		}
		return result, nil
	}
	result := evaluator.defaultResult
	if result.Provenance == (intent.EvaluatorProvenance{}) {
		result.Provenance = testEvaluatorProvenance
	}
	return result, nil
}
