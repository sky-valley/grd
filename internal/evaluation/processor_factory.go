package evaluation

import (
	"context"
	"errors"

	"github.com/sky-valley/grd/internal/intent"
)

type PromotionService interface {
	CurrentIntent(ctx context.Context, repoID string) (intent.Revision, error)
	Promote(ctx context.Context, repoID string, request intent.PromoteRequest) (intent.Promoted, error)
}

type PendingService interface {
	PolicyService
	PromotionService
}

type PendingProcessorFactory interface {
	Build(service PendingService, content RepositoryContent) (PendingProcessor, error)
}

type policyProcessorFactory struct {
	evaluator Evaluator
}

func NewPolicyProcessorFactory(evaluator Evaluator) PendingProcessorFactory {
	return policyProcessorFactory{evaluator: evaluator}
}

func (factory policyProcessorFactory) Build(service PendingService, content RepositoryContent) (PendingProcessor, error) {
	if factory.evaluator == nil {
		return nil, errors.New("policy processor factory requires an evaluator")
	}
	inputs, err := NewRepositoryEvaluationInputSource(content)
	if err != nil {
		return nil, err
	}
	return NewPolicyProcessor(service, factory.evaluator, inputs)
}
