package intentservice

import (
	"context"
	"errors"
	"strings"

	"github.com/sky-valley/grd/internal/intent"
)

var ErrRepositoryNotFound = errors.New("repository not found")
var ErrRepositoryAlreadyInitialized = errors.New("repository intent is already initialized")

type Repository interface {
	CurrentIntent() intent.Revision
	Propose(ctx context.Context, proposal intent.Proposal) (intent.Proposed, error)
	Amend(ctx context.Context, request intent.AmendRequest) (intent.Amended, error)
	ReconcileDependent(ctx context.Context, request intent.ReconcileDependentRequest) (intent.ReconciledDependent, error)
	RebaseHeldVersion(ctx context.Context, request intent.RebaseHeldVersionRequest) (intent.RebasedHeldVersion, error)
	Promote(ctx context.Context, request intent.PromoteRequest) (intent.Promoted, error)
	Promotion(ctx context.Context, versionID intent.VersionID) (intent.Promoted, bool, error)
	PendingEvaluations(ctx context.Context, query intent.PendingEvaluationQuery) (intent.PendingEvaluationPage, error)
	RunnableEvaluations(ctx context.Context, query intent.PendingEvaluationQuery) (intent.PendingEvaluationPage, error)
	Evaluation(ctx context.Context, versionID intent.VersionID) (intent.Evaluation, bool, error)
	EvaluationContext(ctx context.Context, versionID intent.VersionID) (intent.EvaluationContext, error)
	RecordEvaluation(ctx context.Context, evaluation intent.Evaluation) (intent.Evaluation, error)
	PendingRequirements(ctx context.Context, query intent.PendingRequirementQuery) (intent.PendingRequirementPage, error)
	UnresolvedRequirements(ctx context.Context, versionID intent.VersionID) ([]intent.Requirement, error)
	RecordRequirementResponse(ctx context.Context, request intent.RequirementResponseRequest) (intent.RequirementResponse, error)
	RecordReconciliationConflict(ctx context.Context, request intent.ReconciliationConflictRequest) (intent.ReconciliationConflictInspection, error)
	ResolveReconciliationConflict(ctx context.Context, request intent.ResolveReconciliationConflictRequest) (intent.ResolvedReconciliationConflict, error)
	ReconciliationConflict(ctx context.Context, id intent.ConflictID) (intent.ReconciliationConflictInspection, bool, error)
	ReconciliationConflicts(ctx context.Context, query intent.ReconciliationConflictQuery) (intent.ReconciliationConflictPage, error)
	InspectChange(ctx context.Context, id intent.ChangeID) (intent.ChangeInspection, error)
	Versions(ctx context.Context, query intent.VersionQuery) (intent.VersionPage, error)
}

type Repositories interface {
	Resolve(ctx context.Context, repoID string) (Repository, error)
	Bootstrap(ctx context.Context, repoID string, content intent.ContentRef) (intent.Revision, error)
}

type Proposal struct {
	IdempotencyKey string
	BaseIntent     intent.RevisionID
	Content        intent.ContentRef
	Producer       string
	Dependencies   []intent.VersionID
}

type AmendmentRequest struct {
	IdempotencyKey  string
	ChangeID        intent.ChangeID
	ExpectedVersion intent.VersionID
	Content         intent.ContentRef
	Producer        string
	Rationale       string
}

type DependentReconciliationRequest struct {
	IdempotencyKey     string
	ExpectedVersion    intent.VersionID
	ReplacedDependency intent.VersionID
	AcceptedVersion    intent.VersionID
	ExpectedIntent     intent.RevisionID
	Content            intent.ContentRef
	Producer           string
	Rationale          string
}

type HeldVersionRebaseRequest struct {
	IdempotencyKey  string
	ExpectedVersion intent.VersionID
	ExpectedIntent  intent.RevisionID
	Content         intent.ContentRef
	Producer        string
	Rationale       string
}

type ReconciliationConflictRequest struct {
	IdempotencyKey    string
	FromVersion       intent.VersionID
	ToVersion         intent.VersionID
	DescendantVersion intent.VersionID
	ExpectedIntent    intent.RevisionID
	ReportedBy        string
	AffectedPaths     []string
}

type ReconciliationResolutionRequest struct {
	IdempotencyKey  string
	ConflictID      intent.ConflictID
	ExpectedVersion intent.VersionID
	ExpectedIntent  intent.RevisionID
	Content         intent.ContentRef
	Producer        string
	ResolvedBy      string
	Rationale       string
}

type Service struct {
	repositories Repositories
}

func New(repositories Repositories) *Service {
	return &Service{repositories: repositories}
}

func (service *Service) CurrentIntent(ctx context.Context, repoID string) (intent.Revision, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.Revision{}, err
	}
	return repository.CurrentIntent(), nil
}

func (service *Service) PendingEvaluations(ctx context.Context, repoID string, query intent.PendingEvaluationQuery) (intent.PendingEvaluationPage, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.PendingEvaluationPage{}, err
	}
	return repository.PendingEvaluations(ctx, query)
}

func (service *Service) RunnableEvaluations(ctx context.Context, repoID string, query intent.PendingEvaluationQuery) (intent.PendingEvaluationPage, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.PendingEvaluationPage{}, err
	}
	return repository.RunnableEvaluations(ctx, query)
}

func (service *Service) Evaluation(ctx context.Context, repoID string, versionID intent.VersionID) (intent.Evaluation, bool, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.Evaluation{}, false, err
	}
	return repository.Evaluation(ctx, versionID)
}

func (service *Service) EvaluationContext(ctx context.Context, repoID string, versionID intent.VersionID) (intent.EvaluationContext, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.EvaluationContext{}, err
	}
	return repository.EvaluationContext(ctx, versionID)
}

func (service *Service) RecordEvaluation(ctx context.Context, repoID string, evaluation intent.Evaluation) (intent.Evaluation, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.Evaluation{}, err
	}
	return repository.RecordEvaluation(ctx, evaluation)
}

func (service *Service) PendingRequirements(ctx context.Context, repoID string, query intent.PendingRequirementQuery) (intent.PendingRequirementPage, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.PendingRequirementPage{}, err
	}
	return repository.PendingRequirements(ctx, query)
}

func (service *Service) UnresolvedRequirements(ctx context.Context, repoID string, versionID intent.VersionID) ([]intent.Requirement, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return nil, err
	}
	return repository.UnresolvedRequirements(ctx, versionID)
}

func (service *Service) RecordRequirementResponse(ctx context.Context, repoID string, request intent.RequirementResponseRequest) (intent.RequirementResponse, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.RequirementResponse{}, err
	}
	return repository.RecordRequirementResponse(ctx, request)
}

func (service *Service) Bootstrap(ctx context.Context, repoID string, content intent.ContentRef) (intent.Revision, error) {
	return service.repositories.Bootstrap(ctx, repoID, content)
}

func (service *Service) Propose(ctx context.Context, repoID string, proposal Proposal) (intent.Proposed, error) {
	producer := strings.TrimSpace(proposal.Producer)
	if producer == "" {
		return intent.Proposed{}, errors.New("proposal producer is not configured")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.Proposed{}, err
	}
	proposed, err := repository.Propose(ctx, intent.Proposal{
		IdempotencyKey: proposal.IdempotencyKey,
		BaseIntent:     proposal.BaseIntent,
		Content:        proposal.Content,
		Producer:       producer,
		Dependencies:   proposal.Dependencies,
	})
	if err != nil {
		return intent.Proposed{}, err
	}
	return proposed, nil
}

func (service *Service) Amend(ctx context.Context, repoID string, amendment AmendmentRequest) (intent.Amended, error) {
	producer := strings.TrimSpace(amendment.Producer)
	rationale := strings.TrimSpace(amendment.Rationale)
	if producer == "" || rationale == "" {
		return intent.Amended{}, errors.New("amendment producer and rationale are required")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.Amended{}, err
	}
	amended, err := repository.Amend(ctx, intent.AmendRequest{
		IdempotencyKey:  amendment.IdempotencyKey,
		ChangeID:        amendment.ChangeID,
		ExpectedVersion: amendment.ExpectedVersion,
		Content:         amendment.Content,
		Producer:        producer,
		Rationale:       rationale,
	})
	if err != nil {
		return intent.Amended{}, err
	}
	return amended, nil
}

func (service *Service) ReconcileDependent(
	ctx context.Context,
	repoID string,
	request DependentReconciliationRequest,
) (intent.ReconciledDependent, error) {
	producer := strings.TrimSpace(request.Producer)
	rationale := strings.TrimSpace(request.Rationale)
	if producer == "" || rationale == "" {
		return intent.ReconciledDependent{}, errors.New("dependent reconciliation producer and rationale are required")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciledDependent{}, err
	}
	reconciled, err := repository.ReconcileDependent(ctx, intent.ReconcileDependentRequest{
		IdempotencyKey:     request.IdempotencyKey,
		ExpectedVersion:    request.ExpectedVersion,
		ReplacedDependency: request.ReplacedDependency,
		AcceptedVersion:    request.AcceptedVersion,
		ExpectedIntent:     request.ExpectedIntent,
		Content:            request.Content,
		Producer:           producer,
		Rationale:          rationale,
	})
	if err != nil {
		return intent.ReconciledDependent{}, err
	}
	return reconciled, nil
}

func (service *Service) RebaseHeldVersion(
	ctx context.Context,
	repoID string,
	request HeldVersionRebaseRequest,
) (intent.RebasedHeldVersion, error) {
	producer := strings.TrimSpace(request.Producer)
	rationale := strings.TrimSpace(request.Rationale)
	if producer == "" || rationale == "" {
		return intent.RebasedHeldVersion{}, errors.New("held version rebase producer and rationale are required")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.RebasedHeldVersion{}, err
	}
	rebased, err := repository.RebaseHeldVersion(ctx, intent.RebaseHeldVersionRequest{
		IdempotencyKey:  request.IdempotencyKey,
		ExpectedVersion: request.ExpectedVersion,
		ExpectedIntent:  request.ExpectedIntent,
		Content:         request.Content,
		Producer:        producer,
		Rationale:       rationale,
	})
	if err != nil {
		return intent.RebasedHeldVersion{}, err
	}
	return rebased, nil
}

func (service *Service) InspectChange(ctx context.Context, repoID string, changeID intent.ChangeID) (intent.ChangeInspection, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ChangeInspection{}, err
	}
	return repository.InspectChange(ctx, changeID)
}

func (service *Service) RecordReconciliationConflict(ctx context.Context, repoID string, request ReconciliationConflictRequest) (intent.ReconciliationConflictInspection, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciliationConflictInspection{}, err
	}
	expectedIntent := request.ExpectedIntent
	if expectedIntent == "" {
		promoted, found, err := repository.Promotion(ctx, request.ToVersion)
		if err != nil {
			return intent.ReconciliationConflictInspection{}, err
		}
		if !found {
			return intent.ReconciliationConflictInspection{}, intent.ErrVersionNotPromoted
		}
		expectedIntent = promoted.Intent.ID
	}
	return repository.RecordReconciliationConflict(ctx, intent.ReconciliationConflictRequest{
		IdempotencyKey:    request.IdempotencyKey,
		FromVersion:       request.FromVersion,
		ToVersion:         request.ToVersion,
		DescendantVersion: request.DescendantVersion,
		ExpectedIntent:    expectedIntent,
		ReportedBy:        strings.TrimSpace(request.ReportedBy),
		AffectedPaths:     request.AffectedPaths,
	})
}

func (service *Service) ResolveReconciliationConflict(ctx context.Context, repoID string, request ReconciliationResolutionRequest) (intent.ResolvedReconciliationConflict, error) {
	producer := strings.TrimSpace(request.Producer)
	resolvedBy := strings.TrimSpace(request.ResolvedBy)
	rationale := strings.TrimSpace(request.Rationale)
	if producer == "" || resolvedBy == "" || rationale == "" {
		return intent.ResolvedReconciliationConflict{}, errors.New("resolution producer, actor, and rationale are required")
	}
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ResolvedReconciliationConflict{}, err
	}
	resolved, err := repository.ResolveReconciliationConflict(ctx, intent.ResolveReconciliationConflictRequest{
		IdempotencyKey:  request.IdempotencyKey,
		ConflictID:      request.ConflictID,
		ExpectedVersion: request.ExpectedVersion,
		ExpectedIntent:  request.ExpectedIntent,
		Content:         request.Content,
		Producer:        producer,
		ResolvedBy:      resolvedBy,
		Rationale:       rationale,
	})
	if err != nil {
		return intent.ResolvedReconciliationConflict{}, err
	}
	return resolved, nil
}

func (service *Service) Promote(ctx context.Context, repoID string, request intent.PromoteRequest) (intent.Promoted, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.Promoted{}, err
	}
	return repository.Promote(ctx, request)
}

func (service *Service) ReconciliationConflict(ctx context.Context, repoID string, conflictID intent.ConflictID) (intent.ReconciliationConflictInspection, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciliationConflictInspection{}, err
	}
	conflict, found, err := repository.ReconciliationConflict(ctx, conflictID)
	if err != nil {
		return intent.ReconciliationConflictInspection{}, err
	}
	if !found {
		return intent.ReconciliationConflictInspection{}, intent.ErrReconciliationConflictNotFound
	}
	return conflict, nil
}

func (service *Service) ReconciliationConflicts(ctx context.Context, repoID string, query intent.ReconciliationConflictQuery) (intent.ReconciliationConflictPage, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.ReconciliationConflictPage{}, err
	}
	return repository.ReconciliationConflicts(ctx, query)
}

func (service *Service) Versions(ctx context.Context, repoID string, query intent.VersionQuery) (intent.VersionPage, error) {
	repository, err := service.resolve(ctx, repoID)
	if err != nil {
		return intent.VersionPage{}, err
	}
	return repository.Versions(ctx, query)
}

func (service *Service) resolve(ctx context.Context, repoID string) (Repository, error) {
	repository, err := service.repositories.Resolve(ctx, repoID)
	if err != nil {
		return nil, err
	}
	return repository, nil
}
