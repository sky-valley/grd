package evaluation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/principal"
)

const (
	purposePath    = ".grd/purpose.md"
	prioritiesPath = ".grd/priorities.md"
)

type RepositoryContent interface {
	ReadText(ctx context.Context, repoID string, content intent.ContentRef, path string) (string, error)
	Compare(ctx context.Context, repoID string, base, candidate intent.ContentRef) (string, error)
}

type EvaluationInput struct {
	Purpose        string
	Priorities     string
	ChangeEvidence string
	Policies       []Policy
}

type EvaluationInputSource interface {
	Load(ctx context.Context, repoID string, evaluation intent.EvaluationContext) (EvaluationInput, error)
}

type RepositoryEvaluationInputSource struct {
	content RepositoryContent
}

func NewRepositoryEvaluationInputSource(content RepositoryContent) (*RepositoryEvaluationInputSource, error) {
	if content == nil {
		return nil, errors.New("repository evaluation input source requires content")
	}
	return &RepositoryEvaluationInputSource{content: content}, nil
}

func (source *RepositoryEvaluationInputSource) Load(ctx context.Context, repoID string, evaluation intent.EvaluationContext) (EvaluationInput, error) {
	governing := evaluation.GoverningIntent.Content
	purpose, err := source.content.ReadText(ctx, repoID, governing, purposePath)
	if err != nil {
		return EvaluationInput{}, fmt.Errorf("read governing repository purpose: %w", err)
	}
	if strings.TrimSpace(purpose) == "" {
		return EvaluationInput{}, errors.New("governing repository purpose is empty")
	}
	priorities, err := source.content.ReadText(ctx, repoID, governing, prioritiesPath)
	if err != nil {
		return EvaluationInput{}, fmt.Errorf("read governing repository priorities: %w", err)
	}
	policies, err := parsePolicies(priorities)
	if err != nil {
		return EvaluationInput{}, fmt.Errorf("parse governing repository priorities: %w", err)
	}
	evidence, err := source.content.Compare(ctx, repoID, governing, evaluation.Version.Content)
	if err != nil {
		return EvaluationInput{}, fmt.Errorf("compare candidate to governing content: %w", err)
	}
	if strings.TrimSpace(evidence) == "" {
		return EvaluationInput{}, errors.New("candidate comparison produced no evidence")
	}
	return EvaluationInput{
		Purpose:        purpose,
		Priorities:     priorities,
		ChangeEvidence: evidence,
		Policies:       policies,
	}, nil
}

func parsePolicies(priorities string) ([]Policy, error) {
	if strings.TrimSpace(priorities) == "" {
		return nil, errors.New("repository priorities are empty")
	}
	var policies []Policy
	var current *Policy
	finish := func() error {
		if current == nil {
			return nil
		}
		normalized, err := normalizePolicy(*current)
		if err != nil {
			return err
		}
		policies = append(policies, normalized)
		current = nil
		return nil
	}
	for _, rawLine := range strings.Split(priorities, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "## ") {
			if err := finish(); err != nil {
				return nil, err
			}
			current = &Policy{Name: strings.TrimSpace(strings.TrimPrefix(line, "## "))}
			continue
		}
		if current == nil || line == "" {
			continue
		}
		if value, found := strings.CutPrefix(line, "Assignee:"); found {
			if current.Assignee != "" {
				return nil, fmt.Errorf("policy %q repeats Assignee", current.Name)
			}
			current.Assignee = strings.TrimSpace(value)
			continue
		}
		if value, found := strings.CutPrefix(line, "Instruction:"); found {
			if current.Instruction != "" {
				return nil, fmt.Errorf("policy %q repeats Instruction", current.Name)
			}
			current.Instruction = strings.TrimSpace(value)
		}
	}
	if err := finish(); err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, errors.New("repository priorities define no policies")
	}
	seen := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if _, duplicate := seen[policy.Name]; duplicate {
			return nil, fmt.Errorf("policy %q is repeated", policy.Name)
		}
		seen[policy.Name] = struct{}{}
	}
	return policies, nil
}

func normalizePolicy(policy Policy) (Policy, error) {
	policy.Name = strings.TrimSpace(policy.Name)
	policy.Instruction = strings.TrimSpace(policy.Instruction)
	assignee, validAssignee := principal.Canonical(policy.Assignee)
	policy.Assignee = assignee
	if policy.Name == "" || policy.Instruction == "" || policy.Assignee == "" {
		return Policy{}, errors.New("evaluation policy requires name, one-line instruction, and assignee")
	}
	if !validAssignee {
		return Policy{}, errors.New("evaluation policy assignee must be a canonical principal subject")
	}
	if strings.ContainsAny(policy.Instruction, "\r\n") {
		return Policy{}, errors.New("evaluation policy instruction must be one line")
	}
	return policy, nil
}
