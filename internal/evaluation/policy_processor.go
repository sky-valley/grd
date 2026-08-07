package evaluation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sky-valley/grd/internal/intent"
)

type Policy struct {
	Name        string
	Instruction string
	Assignee    string
}

type EvaluationRequest struct {
	RepoID          string
	Version         intent.Version
	GoverningIntent intent.Revision
	Policy          Policy
	Purpose         string
	Priorities      string
	ChangeEvidence  string
}

type EvaluationResult struct {
	RequiresAction bool
	Reason         string
	Evidence       []string
	Provenance     intent.EvaluatorProvenance
}

type Evaluator interface {
	Evaluate(ctx context.Context, request EvaluationRequest) (EvaluationResult, error)
}

type PolicyService interface {
	Evaluation(ctx context.Context, repoID string, versionID intent.VersionID) (intent.Evaluation, bool, error)
	EvaluationContext(ctx context.Context, repoID string, versionID intent.VersionID) (intent.EvaluationContext, error)
	RecordEvaluation(ctx context.Context, repoID string, evaluation intent.Evaluation) (intent.Evaluation, error)
	UnresolvedRequirements(ctx context.Context, repoID string, versionID intent.VersionID) ([]intent.Requirement, error)
	Promote(ctx context.Context, repoID string, request intent.PromoteRequest) (intent.Promoted, error)
}

type PolicyProcessor struct {
	service   PolicyService
	evaluator Evaluator
	inputs    EvaluationInputSource
}

func NewPolicyProcessor(service PolicyService, evaluator Evaluator, inputs EvaluationInputSource) (*PolicyProcessor, error) {
	if service == nil || evaluator == nil || inputs == nil {
		return nil, errors.New("evaluation processor requires service, evaluator, and repository inputs")
	}
	return &PolicyProcessor{service: service, evaluator: evaluator, inputs: inputs}, nil
}

func normalizePolicies(policies []Policy) ([]Policy, error) {
	if len(policies) == 0 {
		return nil, errors.New("evaluation inputs require at least one policy")
	}
	normalized := make([]Policy, len(policies))
	names := make(map[string]struct{}, len(policies))
	for index, policy := range policies {
		var err error
		policy, err = normalizePolicy(policy)
		if err != nil {
			return nil, err
		}
		if _, duplicate := names[policy.Name]; duplicate {
			return nil, errors.New("evaluation policy names must be unique")
		}
		names[policy.Name] = struct{}{}
		normalized[index] = policy
	}
	return normalized, nil
}

func (processor *PolicyProcessor) Process(ctx context.Context, item WorkItem) error {
	recorded, found, err := processor.service.Evaluation(ctx, item.RepoID, item.VersionID)
	if err != nil {
		return fmt.Errorf("read existing evaluation: %w", err)
	}
	if !found {
		recorded, err = processor.evaluate(ctx, item)
		if err != nil {
			return err
		}
	}
	requirements, err := processor.service.UnresolvedRequirements(ctx, item.RepoID, recorded.VersionID)
	if err != nil {
		return fmt.Errorf("read unresolved requirements: %w", err)
	}
	if len(requirements) > 0 {
		return nil
	}
	_, err = processor.service.Promote(ctx, item.RepoID, intent.PromoteRequest{
		VersionID:      recorded.VersionID,
		ExpectedIntent: recorded.GoverningIntent,
	})
	if errors.Is(err, intent.ErrIntentAdvanced) || errors.Is(err, intent.ErrPromotionPending) || errors.Is(err, intent.ErrDependenciesPending) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("promote cleared Version: %w", err)
	}
	return nil
}

func (processor *PolicyProcessor) evaluate(ctx context.Context, item WorkItem) (intent.Evaluation, error) {
	evaluationContext, err := processor.service.EvaluationContext(ctx, item.RepoID, item.VersionID)
	if err != nil {
		return intent.Evaluation{}, fmt.Errorf("read evaluation context: %w", err)
	}
	inputs, err := processor.inputs.Load(ctx, item.RepoID, evaluationContext)
	if err != nil {
		return intent.Evaluation{}, fmt.Errorf("load repository evaluation inputs: %w", err)
	}
	if strings.TrimSpace(inputs.Purpose) == "" || strings.TrimSpace(inputs.Priorities) == "" || strings.TrimSpace(inputs.ChangeEvidence) == "" {
		return intent.Evaluation{}, errors.New("repository evaluation inputs require purpose, priorities, and change evidence")
	}
	policies, err := normalizePolicies(inputs.Policies)
	if err != nil {
		return intent.Evaluation{}, fmt.Errorf("load repository evaluation inputs: %w", err)
	}
	evaluations := make([]intent.PolicyEvaluation, 0, len(policies))
	for _, policy := range policies {
		version := evaluationContext.Version
		version.Dependencies = slices.Clone(version.Dependencies)
		evaluation, err := processor.evaluator.Evaluate(ctx, EvaluationRequest{
			RepoID:          item.RepoID,
			Version:         version,
			GoverningIntent: evaluationContext.GoverningIntent,
			Policy:          policy,
			Purpose:         inputs.Purpose,
			Priorities:      inputs.Priorities,
			ChangeEvidence:  inputs.ChangeEvidence,
		})
		if err != nil {
			return intent.Evaluation{}, fmt.Errorf("evaluate policy %q: %w", policy.Name, err)
		}
		if err := validateEvaluation(evaluation); err != nil {
			return intent.Evaluation{}, fmt.Errorf("evaluate policy %q: %w", policy.Name, err)
		}
		evaluations = append(evaluations, intent.PolicyEvaluation{
			Policy:         policy.Name,
			Instruction:    policy.Instruction,
			Assignee:       policy.Assignee,
			Provenance:     evaluation.Provenance,
			RequiresAction: evaluation.RequiresAction,
			Reason:         evaluation.Reason,
			Evidence:       slices.Clone(evaluation.Evidence),
		})
	}
	result := intent.Evaluation{
		VersionID:         evaluationContext.Version.ID,
		GoverningIntent:   evaluationContext.GoverningIntent.ID,
		PolicyEvaluations: evaluations,
	}
	recorded, err := processor.service.RecordEvaluation(ctx, item.RepoID, result)
	if errors.Is(err, intent.ErrEvaluationAlreadyRecorded) {
		existing, found, readErr := processor.service.Evaluation(ctx, item.RepoID, item.VersionID)
		if readErr != nil {
			return intent.Evaluation{}, fmt.Errorf("read concurrently recorded evaluation: %w", readErr)
		}
		if !found {
			return intent.Evaluation{}, fmt.Errorf("record evaluation: %w", err)
		}
		return existing, nil
	}
	if err != nil {
		return intent.Evaluation{}, fmt.Errorf("record evaluation: %w", err)
	}
	return recorded, nil
}

func validateEvaluation(evaluation EvaluationResult) error {
	if strings.TrimSpace(evaluation.Reason) == "" || len(evaluation.Evidence) == 0 ||
		strings.TrimSpace(evaluation.Provenance.Evaluator) == "" || strings.TrimSpace(evaluation.Provenance.ContractRevision) == "" {
		return errors.New("evaluation requires reason, evidence, and evaluator provenance")
	}
	if evaluation.Provenance.Evaluator != strings.TrimSpace(evaluation.Provenance.Evaluator) ||
		evaluation.Provenance.ContractRevision != strings.TrimSpace(evaluation.Provenance.ContractRevision) ||
		strings.ContainsAny(evaluation.Provenance.Evaluator+evaluation.Provenance.ContractRevision, "\r\n") {
		return errors.New("evaluation evaluator provenance must be canonical one-line text")
	}
	for _, evidence := range evaluation.Evidence {
		if strings.TrimSpace(evidence) == "" {
			return errors.New("evaluation evidence must not be empty")
		}
	}
	return nil
}
