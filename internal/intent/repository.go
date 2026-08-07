package intent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
)

var ErrIntentAdvanced = errors.New("canonical intent advanced")
var ErrIntentNotFound = errors.New("intent not found")
var ErrChangeNotFound = errors.New("change not found")
var ErrVersionNotFound = errors.New("change version not found")
var ErrContentNotAdmissible = errors.New("content cannot be admitted by the repository engine")
var ErrIdempotencyConflict = errors.New("idempotency key already used for a different operation")
var ErrPromotionPending = errors.New("another promotion is pending reconciliation")
var ErrDependenciesPending = errors.New("change dependencies are not promoted")
var ErrEvaluationAlreadyRecorded = errors.New("evaluation is already recorded differently")
var ErrRequirementRequired = errors.New("unresolved requirement is required")

type ContentRef struct {
	Engine   string
	Revision string
}

type RevisionID string

type ChangeID string

type VersionID string

type PromotionID string

type Revision struct {
	ID         RevisionID
	PreviousID RevisionID
	Content    ContentRef
}

type Change struct {
	ID ChangeID
}

type Version struct {
	ID           VersionID
	ChangeID     ChangeID
	BaseIntent   RevisionID
	Content      ContentRef
	Producer     string
	Dependencies []VersionID
}

type Promotion struct {
	ID         PromotionID
	FromIntent RevisionID
	ToIntent   RevisionID
	VersionID  VersionID
}

type Proposal struct {
	IdempotencyKey string
	BaseIntent     RevisionID
	Content        ContentRef
	Producer       string
	Dependencies   []VersionID
}

type Proposed struct {
	Change  Change
	Version Version
}

type PendingEvaluationQuery struct {
	After VersionID
	Limit int
}

type PendingEvaluationPage struct {
	Versions   []Version
	NextCursor VersionID
}

type PromoteRequest struct {
	VersionID      VersionID
	ExpectedIntent RevisionID
}

type Promoted struct {
	Promotion Promotion
	Intent    Revision
}

type VersionQuery struct {
	ChangeID ChangeID
	After    VersionID
	Limit    int
}

type VersionPage struct {
	Versions   []Version
	NextCursor VersionID
}

type ChangeInspection struct {
	Change          Change
	LatestVersion   Version
	LatestAmendment *Amendment
	LatestPromotion *Promoted
}

type PreparedPromotion struct {
	Promotion Promotion
	Intent    Revision
}

type ContentAdmission interface {
	Admit(ctx context.Context, versionID VersionID, content ContentRef) error
}

type TrunkProjection interface {
	Current(ctx context.Context) (ContentRef, error)
	Advance(ctx context.Context, expected, next ContentRef) error
}

type Repository struct {
	changeMu             sync.Mutex
	promotionMu          sync.Mutex
	stateMu              sync.RWMutex
	current              Revision
	admission            ContentAdmission
	projection           TrunkProjection
	intents              IntentStore
	changes              ChangeStore
	pending              PendingEvaluationStore
	evaluations          EvaluationStore
	requirementResponses RequirementResponseStore
	amendments           AmendmentStore
	reconciliations      DependentReconciliationStore
	rebases              HeldVersionRebaseStore
	promotions           PromotionJournal
	conflicts            ReconciliationConflictStore
	conflict             *ProjectionConflict
}

// NewEphemeralRepository creates a repository whose ledger is held only in
// memory. It is useful for tests and disposable embedding; production callers
// should supply durable storage through OpenRepository.
func NewEphemeralRepository(initial ContentRef, admission ContentAdmission, projection TrunkProjection) (*Repository, error) {
	return OpenRepository(context.Background(), initial, &transientLedger{}, admission, projection)
}

func OpenRepository(ctx context.Context, initial ContentRef, ledger Ledger, admission ContentAdmission, projection TrunkProjection) (*Repository, error) {
	if initial.Engine == "" || initial.Revision == "" {
		return nil, errors.New("initial content reference requires engine and revision")
	}
	if ledger == nil {
		return nil, errors.New("intent ledger is required")
	}
	if admission == nil {
		return nil, errors.New("content admission is required")
	}
	if projection == nil {
		return nil, errors.New("trunk projection is required")
	}

	current, found, err := ledger.CurrentIntent(ctx)
	if err != nil {
		return nil, fmt.Errorf("read current intent: %w", err)
	}
	if !found {
		initialID, err := newID("intent")
		if err != nil {
			return nil, fmt.Errorf("create initial intent id: %w", err)
		}
		current = Revision{
			ID:      RevisionID(initialID),
			Content: initial,
		}
		if err := ledger.Initialize(ctx, current); err != nil {
			return nil, fmt.Errorf("initialize intent ledger: %w", err)
		}
	}

	repository := &Repository{
		current:              current,
		admission:            admission,
		projection:           projection,
		intents:              ledger,
		changes:              ledger,
		pending:              ledger,
		evaluations:          ledger,
		requirementResponses: ledger,
		amendments:           ledger,
		reconciliations:      ledger,
		rebases:              ledger,
		promotions:           ledger,
		conflicts:            ledger,
	}
	if err := repository.Reconcile(ctx); err != nil {
		var conflict *ProjectionConflict
		if !errors.As(err, &conflict) {
			return nil, fmt.Errorf("reconcile repository: %w", err)
		}
	}
	return repository, nil
}

func (repository *Repository) CurrentIntent() Revision {
	repository.stateMu.RLock()
	defer repository.stateMu.RUnlock()
	return repository.current
}

func (repository *Repository) ProjectionConflict() (ProjectionConflict, bool) {
	repository.stateMu.RLock()
	defer repository.stateMu.RUnlock()
	if repository.conflict == nil {
		return ProjectionConflict{}, false
	}
	return *repository.conflict, true
}

func (repository *Repository) Change(ctx context.Context, id ChangeID) (Change, bool, error) {
	return repository.changes.Change(ctx, id)
}

func (repository *Repository) InspectChange(ctx context.Context, id ChangeID) (ChangeInspection, error) {
	change, found, err := repository.changes.Change(ctx, id)
	if err != nil {
		return ChangeInspection{}, fmt.Errorf("read change: %w", err)
	}
	if !found {
		return ChangeInspection{}, ErrChangeNotFound
	}
	latest, found, err := repository.changes.LatestVersion(ctx, id)
	if err != nil {
		return ChangeInspection{}, fmt.Errorf("read latest change version: %w", err)
	}
	if !found {
		return ChangeInspection{}, errors.New("change has no versions")
	}
	inspection := ChangeInspection{Change: change, LatestVersion: latest}
	amendment, found, err := repository.amendments.Amendment(ctx, latest.ID)
	if err != nil {
		return ChangeInspection{}, fmt.Errorf("read latest version amendment: %w", err)
	}
	if found {
		inspection.LatestAmendment = &amendment
	}
	promoted, found, err := repository.promotions.CompletedPromotion(ctx, latest.ID)
	if err != nil {
		return ChangeInspection{}, fmt.Errorf("read latest version promotion: %w", err)
	}
	if found {
		inspection.LatestPromotion = &promoted
	}
	return inspection, nil
}

func (repository *Repository) Promotion(ctx context.Context, versionID VersionID) (Promoted, bool, error) {
	promoted, found, err := repository.promotions.CompletedPromotion(ctx, versionID)
	if err != nil {
		return Promoted{}, false, fmt.Errorf("read version promotion: %w", err)
	}
	return promoted, found, nil
}

func (repository *Repository) Versions(ctx context.Context, query VersionQuery) (VersionPage, error) {
	if query.ChangeID == "" {
		return VersionPage{}, errors.New("change id is required")
	}
	if query.Limit < 1 || query.Limit > 100 {
		return VersionPage{}, errors.New("version page limit must be between 1 and 100")
	}
	if _, found, err := repository.changes.Change(ctx, query.ChangeID); err != nil {
		return VersionPage{}, fmt.Errorf("read change: %w", err)
	} else if !found {
		return VersionPage{}, ErrChangeNotFound
	}
	versions, more, err := repository.changes.Versions(ctx, query.ChangeID, query.After, query.Limit)
	if err != nil {
		return VersionPage{}, fmt.Errorf("read change versions: %w", err)
	}
	page := VersionPage{Versions: versions}
	if more && len(versions) > 0 {
		page.NextCursor = versions[len(versions)-1].ID
	}
	return page, nil
}

func (repository *Repository) PendingEvaluations(ctx context.Context, query PendingEvaluationQuery) (PendingEvaluationPage, error) {
	if query.Limit < 1 || query.Limit > 100 {
		return PendingEvaluationPage{}, errors.New("pending evaluation page limit must be between 1 and 100")
	}
	versions, more, err := repository.pending.PendingEvaluations(ctx, query.After, query.Limit)
	if err != nil {
		return PendingEvaluationPage{}, fmt.Errorf("read pending evaluations: %w", err)
	}
	page := PendingEvaluationPage{Versions: versions}
	if more && len(versions) > 0 {
		page.NextCursor = versions[len(versions)-1].ID
	}
	return page, nil
}

func (repository *Repository) Propose(ctx context.Context, proposal Proposal) (Proposed, error) {
	if proposal.IdempotencyKey == "" {
		return Proposed{}, errors.New("proposal idempotency key is required")
	}
	if proposal.Content.Engine == "" || proposal.Content.Revision == "" {
		return Proposed{}, errors.New("proposed content reference requires engine and revision")
	}
	if proposal.Producer == "" {
		return Proposed{}, errors.New("proposal producer is required")
	}

	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()

	existing, found, err := repository.changes.ProposalByIdempotencyKey(ctx, proposal.IdempotencyKey)
	if err != nil {
		return Proposed{}, fmt.Errorf("read proposal idempotency record: %w", err)
	}
	if found {
		if existing.Version.BaseIntent != proposal.BaseIntent || existing.Version.Content != proposal.Content || existing.Version.Producer != proposal.Producer || !slices.Equal(existing.Version.Dependencies, proposal.Dependencies) {
			return Proposed{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	_, found, err = repository.intents.Revision(ctx, proposal.BaseIntent)
	if err != nil {
		return Proposed{}, fmt.Errorf("read proposal base intent: %w", err)
	}
	if !found {
		return Proposed{}, ErrIntentNotFound
	}
	seenDependencies := make(map[VersionID]struct{}, len(proposal.Dependencies))
	for _, dependencyID := range proposal.Dependencies {
		if dependencyID == "" {
			return Proposed{}, errors.New("proposal dependency version id is required")
		}
		if _, duplicate := seenDependencies[dependencyID]; duplicate {
			return Proposed{}, errors.New("proposal dependencies must be unique")
		}
		seenDependencies[dependencyID] = struct{}{}
		if _, found, err := repository.changes.Version(ctx, dependencyID); err != nil {
			return Proposed{}, fmt.Errorf("read proposal dependency: %w", err)
		} else if !found {
			return Proposed{}, ErrVersionNotFound
		}
	}

	changeID, err := newID("change")
	if err != nil {
		return Proposed{}, fmt.Errorf("create change id: %w", err)
	}
	versionID, err := newID("version")
	if err != nil {
		return Proposed{}, fmt.Errorf("create version id: %w", err)
	}

	version := Version{
		ID:           VersionID(versionID),
		ChangeID:     ChangeID(changeID),
		BaseIntent:   proposal.BaseIntent,
		Content:      proposal.Content,
		Producer:     proposal.Producer,
		Dependencies: slices.Clone(proposal.Dependencies),
	}
	if err := repository.admission.Admit(ctx, version.ID, version.Content); err != nil {
		return Proposed{}, fmt.Errorf("admit proposed content: %w", err)
	}
	change := Change{ID: version.ChangeID}
	if err := repository.changes.RecordProposal(ctx, proposal.IdempotencyKey, change, version); err != nil {
		return Proposed{}, fmt.Errorf("record proposal: %w", err)
	}

	return Proposed{Change: change, Version: version}, nil
}

func (repository *Repository) Promote(ctx context.Context, request PromoteRequest) (Promoted, error) {
	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()

	completed, found, err := repository.promotions.CompletedPromotion(ctx, request.VersionID)
	if err != nil {
		return Promoted{}, fmt.Errorf("read completed promotion: %w", err)
	}
	if found {
		if completed.Promotion.FromIntent != request.ExpectedIntent {
			return Promoted{}, ErrIntentAdvanced
		}
		repository.stateMu.Lock()
		repository.current = completed.Intent
		repository.conflict = nil
		repository.stateMu.Unlock()
		return completed, nil
	}
	pending, found, err := repository.promotions.PendingPromotion(ctx)
	if err != nil {
		return Promoted{}, fmt.Errorf("read pending promotion: %w", err)
	}
	if found {
		if pending.Promotion.VersionID != request.VersionID || pending.Promotion.FromIntent != request.ExpectedIntent {
			return Promoted{}, ErrPromotionPending
		}
		return repository.reconcileAndRemember(ctx, pending)
	}

	version, found, err := repository.changes.Version(ctx, request.VersionID)
	if err != nil {
		return Promoted{}, fmt.Errorf("read change version: %w", err)
	}
	if !found {
		return Promoted{}, ErrVersionNotFound
	}
	evaluation, evaluationFound, err := repository.evaluations.Evaluation(ctx, version.ID)
	if err != nil {
		return Promoted{}, fmt.Errorf("read Version policy evaluation: %w", err)
	}
	if evaluationFound && evaluation.GoverningIntent != request.ExpectedIntent {
		return Promoted{}, ErrIntentAdvanced
	}
	if evaluationFound {
		responses, err := repository.requirementResponses.RequirementResponses(ctx, version.ID)
		if err != nil {
			return Promoted{}, fmt.Errorf("read Version requirement responses: %w", err)
		}
		if len(unresolvedRequirements(evaluation, responses)) > 0 {
			return Promoted{}, ErrRequirementRequired
		}
	}
	dependencyPromotions := make([]Promoted, 0, len(version.Dependencies))
	for _, dependencyID := range version.Dependencies {
		promoted, found, err := repository.promotions.CompletedPromotion(ctx, dependencyID)
		if err != nil {
			return Promoted{}, fmt.Errorf("read dependency promotion: %w", err)
		}
		if !found {
			return Promoted{}, ErrDependenciesPending
		}
		dependencyPromotions = append(dependencyPromotions, promoted)
	}
	current, found, err := repository.intents.CurrentIntent(ctx)
	if err != nil {
		return Promoted{}, fmt.Errorf("read current intent: %w", err)
	}
	if !found {
		return Promoted{}, errors.New("intent ledger is not initialized")
	}
	if request.ExpectedIntent != current.ID {
		return Promoted{}, ErrIntentAdvanced
	}
	if version.BaseIntent != current.ID {
		currentProducedByDependency := false
		for _, promoted := range dependencyPromotions {
			if promoted.Intent.ID == current.ID {
				currentProducedByDependency = true
				break
			}
		}
		if !currentProducedByDependency {
			return Promoted{}, ErrIntentAdvanced
		}
	}

	nextIntentID, err := newID("intent")
	if err != nil {
		return Promoted{}, fmt.Errorf("create next intent id: %w", err)
	}
	promotionID, err := newID("promotion")
	if err != nil {
		return Promoted{}, fmt.Errorf("create promotion id: %w", err)
	}

	nextIntent := Revision{
		ID:         RevisionID(nextIntentID),
		PreviousID: current.ID,
		Content:    version.Content,
	}
	promotion := Promotion{
		ID:         PromotionID(promotionID),
		FromIntent: current.ID,
		ToIntent:   nextIntent.ID,
		VersionID:  version.ID,
	}
	prepared := PreparedPromotion{Promotion: promotion, Intent: nextIntent}
	if err := repository.promotions.PreparePromotion(ctx, prepared); err != nil {
		return Promoted{}, fmt.Errorf("prepare promotion: %w", err)
	}
	return repository.reconcileAndRemember(ctx, prepared)
}

func (repository *Repository) Reconcile(ctx context.Context) error {
	repository.promotionMu.Lock()
	defer repository.promotionMu.Unlock()

	pending, found, err := repository.promotions.PendingPromotion(ctx)
	if err != nil {
		return fmt.Errorf("read pending promotion: %w", err)
	}
	if !found {
		repository.setProjectionConflict(nil)
		return nil
	}
	_, err = repository.reconcileAndRemember(ctx, pending)
	return err
}

func (repository *Repository) reconcileAndRemember(ctx context.Context, prepared PreparedPromotion) (Promoted, error) {
	promoted, err := repository.reconcilePrepared(ctx, prepared)
	if err != nil {
		var conflict *ProjectionConflict
		if errors.As(err, &conflict) {
			repository.setProjectionConflict(conflict)
		}
		return Promoted{}, err
	}
	repository.setProjectionConflict(nil)
	return promoted, nil
}

func (repository *Repository) reconcilePrepared(ctx context.Context, prepared PreparedPromotion) (Promoted, error) {
	from, found, err := repository.intents.Revision(ctx, prepared.Promotion.FromIntent)
	if err != nil {
		return Promoted{}, fmt.Errorf("read promotion base intent: %w", err)
	}
	if !found {
		return Promoted{}, ErrIntentNotFound
	}
	actual, err := repository.projection.Current(ctx)
	if err != nil {
		return Promoted{}, fmt.Errorf("read trunk projection: %w", err)
	}
	if actual == from.Content {
		advanceErr := repository.projection.Advance(ctx, from.Content, prepared.Intent.Content)
		if advanceErr == nil {
			actual = prepared.Intent.Content
		} else if !errors.Is(advanceErr, ErrIntentAdvanced) {
			return Promoted{}, fmt.Errorf("advance trunk projection: %w", advanceErr)
		} else {
			actual, err = repository.projection.Current(ctx)
			if err != nil {
				return Promoted{}, fmt.Errorf("reread trunk projection after compare-and-swap failure: %w", err)
			}
			if actual == from.Content {
				return Promoted{}, fmt.Errorf("advance trunk projection: %w", advanceErr)
			}
		}
	}
	if actual != prepared.Intent.Content {
		return Promoted{}, &ProjectionConflict{
			Prepared: prepared,
			Expected: from.Content,
			Actual:   actual,
		}
	}
	if err := repository.promotions.CompletePromotion(ctx, prepared.Promotion.ID); err != nil {
		return Promoted{}, fmt.Errorf("complete promotion: %w", err)
	}

	repository.stateMu.Lock()
	repository.current = prepared.Intent
	repository.stateMu.Unlock()

	return Promoted{
		Promotion: prepared.Promotion,
		Intent:    prepared.Intent,
	}, nil
}

func (repository *Repository) setProjectionConflict(conflict *ProjectionConflict) {
	repository.stateMu.Lock()
	defer repository.stateMu.Unlock()
	repository.conflict = conflict
}

type ProjectionConflict struct {
	Prepared PreparedPromotion
	Expected ContentRef
	Actual   ContentRef
}

func (conflict *ProjectionConflict) Error() string {
	return "trunk projection diverged from the prepared promotion"
}

func newID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(random[:]), nil
}
