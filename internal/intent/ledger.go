package intent

import (
	"context"
	"errors"
	"slices"
	"sync"
)

type IntentStore interface {
	CurrentIntent(ctx context.Context) (Revision, bool, error)
	Revision(ctx context.Context, id RevisionID) (Revision, bool, error)
	Initialize(ctx context.Context, initial Revision) error
}

type ChangeStore interface {
	Change(ctx context.Context, id ChangeID) (Change, bool, error)
	Version(ctx context.Context, id VersionID) (Version, bool, error)
	Dependents(ctx context.Context, id VersionID) ([]Version, error)
	LatestVersion(ctx context.Context, changeID ChangeID) (Version, bool, error)
	Versions(ctx context.Context, changeID ChangeID, after VersionID, limit int) ([]Version, bool, error)
	ProposalByIdempotencyKey(ctx context.Context, key string) (Proposed, bool, error)
	RecordProposal(ctx context.Context, idempotencyKey string, change Change, version Version) error
}

type PendingEvaluationStore interface {
	PendingEvaluations(ctx context.Context, after VersionID, limit int) ([]Version, bool, error)
}

type EvaluationStore interface {
	Evaluation(ctx context.Context, versionID VersionID) (Evaluation, bool, error)
	RunnableEvaluations(ctx context.Context, after VersionID, limit int) ([]Version, bool, error)
	RecordEvaluation(ctx context.Context, evaluation Evaluation) error
}

type RequirementResponseStore interface {
	RequirementResponseByIdempotencyKey(ctx context.Context, key string) (RequirementResponse, bool, error)
	RequirementResponses(ctx context.Context, versionID VersionID) ([]RequirementResponse, error)
	PendingRequirements(ctx context.Context, assignee string, after RequirementCursor, limit int) ([]Requirement, bool, error)
	RecordRequirementResponse(ctx context.Context, key string, response RequirementResponse) error
}

type AmendmentStore interface {
	Amendment(ctx context.Context, toVersion VersionID) (Amendment, bool, error)
	AmendmentByIdempotencyKey(ctx context.Context, key string) (Amended, bool, error)
	RecordAmendment(ctx context.Context, key string, amendment Amendment, version Version) error
}

type DependentReconciliationStore interface {
	DependentReconciliation(ctx context.Context, toVersion VersionID) (DependentReconciliation, bool, error)
	DependentReconciliations(ctx context.Context, after VersionID, limit int) ([]DependentReconciliation, bool, error)
	DependentReconciliationByIdempotencyKey(ctx context.Context, key string) (ReconciledDependent, bool, error)
	RecordDependentReconciliation(ctx context.Context, key string, reconciliation DependentReconciliation, version Version) error
}

type HeldVersionRebaseStore interface {
	HeldVersionRebase(ctx context.Context, toVersion VersionID) (HeldVersionRebase, bool, error)
	HeldVersionRebaseByIdempotencyKey(ctx context.Context, key string) (RebasedHeldVersion, bool, error)
	RecordHeldVersionRebase(ctx context.Context, key string, rebase HeldVersionRebase, version Version) error
}

type PromotionJournal interface {
	PendingPromotion(ctx context.Context) (PreparedPromotion, bool, error)
	CompletedPromotion(ctx context.Context, versionID VersionID) (Promoted, bool, error)
	CompletedPromotionByIntent(ctx context.Context, intentID RevisionID) (Promoted, bool, error)
	PreparePromotion(ctx context.Context, prepared PreparedPromotion) error
	CompletePromotion(ctx context.Context, promotionID PromotionID) error
}

type ReconciliationConflictStore interface {
	ReconciliationConflict(ctx context.Context, id ConflictID) (ReconciliationConflictInspection, bool, error)
	ReconciliationConflicts(ctx context.Context, after ConflictID, limit int) ([]ReconciliationConflictInspection, bool, error)
	ReconciliationConflictByIdempotencyKey(ctx context.Context, key string) (ReconciliationConflictInspection, bool, error)
	RecordReconciliationConflict(ctx context.Context, key string, conflict ReconciliationConflict) error
	ReconciliationResolution(ctx context.Context, conflictID ConflictID) (ReconciliationResolution, bool, error)
	ReconciliationResolutionByIdempotencyKey(ctx context.Context, key string) (ResolvedReconciliationConflict, bool, error)
	RecordReconciliationResolution(ctx context.Context, key string, resolution ReconciliationResolution, version Version) error
}

type Ledger interface {
	IntentStore
	ChangeStore
	PendingEvaluationStore
	EvaluationStore
	RequirementResponseStore
	AmendmentStore
	DependentReconciliationStore
	HeldVersionRebaseStore
	PromotionJournal
	ReconciliationConflictStore
}

type transientLedger struct {
	mu                      sync.RWMutex
	current                 Revision
	revisions               map[RevisionID]Revision
	changes                 map[ChangeID]Change
	versions                map[VersionID]Version
	versionIDs              map[ChangeID][]VersionID
	dependents              map[VersionID][]VersionID
	pendingEvaluations      map[VersionID]struct{}
	evaluationIDs           []VersionID
	evaluations             map[VersionID]Evaluation
	requirementResponses    map[VersionID][]RequirementResponse
	requirementResponseByID map[RequirementResponseID]RequirementResponse
	amendments              map[VersionID]Amendment
	reconciliations         map[VersionID]DependentReconciliation
	reconciliationIDs       []VersionID
	rebases                 map[VersionID]HeldVersionRebase
	promotions              map[PromotionID]Promotion
	prepared                map[PromotionID]PreparedPromotion
	pending                 PromotionID
	completed               map[VersionID]PromotionID
	byIntent                map[RevisionID]PromotionID
	conflicts               map[ConflictID]ReconciliationConflict
	conflictIDs             []ConflictID
	resolutions             map[ConflictID]ReconciliationResolution
	idempotency             map[string]transientIdempotencyRecord
}

type transientIdempotencyOperation uint8

const (
	transientProposalOperation transientIdempotencyOperation = iota + 1
	transientAmendmentOperation
	transientDependentReconciliationOperation
	transientHeldVersionRebaseOperation
	transientReconciliationConflictOperation
	transientReconciliationResolutionOperation
	transientRequirementResponseOperation
)

type transientIdempotencyRecord struct {
	operation     transientIdempotencyOperation
	versionID     VersionID
	conflictID    ConflictID
	requirementID RequirementResponseID
}

func (ledger *transientLedger) CurrentIntent(context.Context) (Revision, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	return ledger.current, ledger.current.ID != "", nil
}

func (ledger *transientLedger) Revision(_ context.Context, id RevisionID) (Revision, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	revision, found := ledger.revisions[id]
	return revision, found, nil
}

func (ledger *transientLedger) Change(_ context.Context, id ChangeID) (Change, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	change, found := ledger.changes[id]
	return change, found, nil
}

func (ledger *transientLedger) Version(_ context.Context, id VersionID) (Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	version, found := ledger.versions[id]
	return cloneVersion(version), found, nil
}

func (ledger *transientLedger) Dependents(_ context.Context, id VersionID) ([]Version, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	ids := ledger.dependents[id]
	versions := make([]Version, 0, len(ids))
	for _, dependentID := range ids {
		versions = append(versions, cloneVersion(ledger.versions[dependentID]))
	}
	return versions, nil
}

func (ledger *transientLedger) Amendment(_ context.Context, toVersion VersionID) (Amendment, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	amendment, found := ledger.amendments[toVersion]
	return amendment, found, nil
}

func (ledger *transientLedger) AmendmentByIdempotencyKey(_ context.Context, key string) (Amended, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return Amended{}, false, nil
	}
	if record.operation != transientAmendmentOperation {
		return Amended{}, false, ErrIdempotencyConflict
	}
	versionID := record.versionID
	amendment, amended := ledger.amendments[versionID]
	if !amended {
		return Amended{}, false, ErrIdempotencyConflict
	}
	version := cloneVersion(ledger.versions[versionID])
	return Amended{Change: ledger.changes[version.ChangeID], Version: version, Amendment: amendment}, true, nil
}

func (ledger *transientLedger) LatestVersion(_ context.Context, changeID ChangeID) (Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	ids := ledger.versionIDs[changeID]
	if len(ids) == 0 {
		return Version{}, false, nil
	}
	return cloneVersion(ledger.versions[ids[len(ids)-1]]), true, nil
}

func (ledger *transientLedger) Versions(_ context.Context, changeID ChangeID, after VersionID, limit int) ([]Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	ids := ledger.versionIDs[changeID]
	start := 0
	if after != "" {
		start = -1
		for index, id := range ids {
			if id == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, ErrVersionNotFound
		}
	}
	end := min(start+limit, len(ids))
	versions := make([]Version, 0, end-start)
	for _, id := range ids[start:end] {
		versions = append(versions, cloneVersion(ledger.versions[id]))
	}
	return versions, end < len(ids), nil
}

func (ledger *transientLedger) PendingEvaluations(_ context.Context, after VersionID, limit int) ([]Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	start := 0
	if after != "" {
		start = -1
		for index, id := range ledger.evaluationIDs {
			if id == after {
				start = index + 1
				break
			}
		}
		if start < 0 {
			return nil, false, ErrVersionNotFound
		}
	}
	versions := make([]Version, 0, limit)
	index := start
	for ; index < len(ledger.evaluationIDs) && len(versions) < limit; index++ {
		id := ledger.evaluationIDs[index]
		if _, pending := ledger.pendingEvaluations[id]; pending {
			versions = append(versions, cloneVersion(ledger.versions[id]))
		}
	}
	for ; index < len(ledger.evaluationIDs); index++ {
		if _, pending := ledger.pendingEvaluations[ledger.evaluationIDs[index]]; pending {
			return versions, true, nil
		}
	}
	return versions, false, nil
}

func (ledger *transientLedger) ProposalByIdempotencyKey(_ context.Context, key string) (Proposed, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return Proposed{}, false, nil
	}
	if record.operation != transientProposalOperation {
		return Proposed{}, false, ErrIdempotencyConflict
	}
	versionID := record.versionID
	version := cloneVersion(ledger.versions[versionID])
	return Proposed{Change: ledger.changes[version.ChangeID], Version: version}, true, nil
}

func (ledger *transientLedger) PendingPromotion(context.Context) (PreparedPromotion, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	prepared, found := ledger.prepared[ledger.pending]
	return prepared, found, nil
}

func (ledger *transientLedger) CompletedPromotion(_ context.Context, versionID VersionID) (Promoted, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	promotionID, found := ledger.completed[versionID]
	if !found {
		return Promoted{}, false, nil
	}
	promotion := ledger.promotions[promotionID]
	return Promoted{Promotion: promotion, Intent: ledger.revisions[promotion.ToIntent]}, true, nil
}

func (ledger *transientLedger) CompletedPromotionByIntent(_ context.Context, intentID RevisionID) (Promoted, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	promotionID, found := ledger.byIntent[intentID]
	if !found {
		return Promoted{}, false, nil
	}
	promotion := ledger.promotions[promotionID]
	return Promoted{Promotion: promotion, Intent: ledger.revisions[promotion.ToIntent]}, true, nil
}

func (ledger *transientLedger) Initialize(_ context.Context, initial Revision) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.current = initial
	ledger.revisions = map[RevisionID]Revision{initial.ID: initial}
	ledger.changes = make(map[ChangeID]Change)
	ledger.versions = make(map[VersionID]Version)
	ledger.versionIDs = make(map[ChangeID][]VersionID)
	ledger.dependents = make(map[VersionID][]VersionID)
	ledger.pendingEvaluations = make(map[VersionID]struct{})
	ledger.evaluationIDs = nil
	ledger.evaluations = make(map[VersionID]Evaluation)
	ledger.requirementResponses = make(map[VersionID][]RequirementResponse)
	ledger.requirementResponseByID = make(map[RequirementResponseID]RequirementResponse)
	ledger.amendments = make(map[VersionID]Amendment)
	ledger.reconciliations = make(map[VersionID]DependentReconciliation)
	ledger.rebases = make(map[VersionID]HeldVersionRebase)
	ledger.promotions = make(map[PromotionID]Promotion)
	ledger.prepared = make(map[PromotionID]PreparedPromotion)
	ledger.completed = make(map[VersionID]PromotionID)
	ledger.byIntent = make(map[RevisionID]PromotionID)
	ledger.conflicts = make(map[ConflictID]ReconciliationConflict)
	ledger.conflictIDs = nil
	ledger.resolutions = make(map[ConflictID]ReconciliationResolution)
	ledger.idempotency = make(map[string]transientIdempotencyRecord)
	return nil
}

func (ledger *transientLedger) RecordProposal(_ context.Context, key string, change Change, version Version) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		existingVersion, versionFound := ledger.versions[existing.versionID]
		existingChange, changeFound := ledger.changes[existingVersion.ChangeID]
		if existing.operation == transientProposalOperation &&
			versionFound && changeFound &&
			existingChange == change && versionsEqual(existingVersion, version) {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if change.ID == "" || version.ID == "" || version.ChangeID != change.ID {
		return errors.New("invalid proposal identity")
	}
	if _, found := ledger.revisions[version.BaseIntent]; !found {
		return errors.New("proposal base intent is not recorded")
	}
	if version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid proposal version")
	}
	if _, found := ledger.changes[change.ID]; found {
		return errors.New("duplicate change id")
	}
	if _, found := ledger.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	seenDependencies := make(map[VersionID]struct{}, len(version.Dependencies))
	for _, dependencyID := range version.Dependencies {
		if dependencyID == "" {
			return errors.New("invalid proposal dependency")
		}
		if _, duplicate := seenDependencies[dependencyID]; duplicate {
			return errors.New("duplicate proposal dependency")
		}
		seenDependencies[dependencyID] = struct{}{}
		if _, found := ledger.versions[dependencyID]; !found {
			return errors.New("proposal dependency is not recorded")
		}
	}
	ledger.changes[change.ID] = change
	ledger.versions[version.ID] = cloneVersion(version)
	ledger.versionIDs[change.ID] = append(ledger.versionIDs[change.ID], version.ID)
	for _, dependencyID := range version.Dependencies {
		ledger.dependents[dependencyID] = append(ledger.dependents[dependencyID], version.ID)
	}
	ledger.idempotency[key] = transientIdempotencyRecord{operation: transientProposalOperation, versionID: version.ID}
	ledger.beginPendingEvaluation(version.ID, "")
	return nil
}

func (ledger *transientLedger) RecordAmendment(_ context.Context, key string, amendment Amendment, version Version) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		existingVersion, versionFound := ledger.versions[existing.versionID]
		existingAmendment, amendmentFound := ledger.amendments[existing.versionID]
		if existing.operation == transientAmendmentOperation &&
			versionFound && amendmentFound &&
			existingAmendment == amendment && versionsEqual(existingVersion, version) {
			return nil
		}
		return ErrIdempotencyConflict
	}
	previous, found := ledger.versions[amendment.FromVersion]
	if !found {
		return ErrVersionNotFound
	}
	ids := ledger.versionIDs[previous.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != previous.ID {
		return ErrVersionAdvanced
	}
	if _, promoted := ledger.completed[previous.ID]; promoted {
		return ErrVersionPromotionStarted
	}
	for _, prepared := range ledger.prepared {
		if prepared.Promotion.VersionID == previous.ID {
			return ErrVersionPromotionStarted
		}
	}
	if version.ID == "" || version.ChangeID != previous.ChangeID || amendment.ToVersion != version.ID || amendment.Rationale == "" {
		return errors.New("invalid amendment identity")
	}
	if version.BaseIntent != previous.BaseIntent || !slices.Equal(version.Dependencies, previous.Dependencies) {
		return errors.New("amendment does not preserve version base and dependencies")
	}
	if version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid amended version")
	}
	if _, found := ledger.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	ledger.versions[version.ID] = cloneVersion(version)
	ledger.versionIDs[version.ChangeID] = append(ledger.versionIDs[version.ChangeID], version.ID)
	for _, dependencyID := range version.Dependencies {
		ledger.dependents[dependencyID] = append(ledger.dependents[dependencyID], version.ID)
	}
	ledger.amendments[version.ID] = amendment
	ledger.idempotency[key] = transientIdempotencyRecord{operation: transientAmendmentOperation, versionID: version.ID}
	ledger.beginPendingEvaluation(version.ID, amendment.FromVersion)
	return nil
}

func (ledger *transientLedger) beginPendingEvaluation(versionID, superseded VersionID) {
	delete(ledger.pendingEvaluations, superseded)
	if _, exists := ledger.pendingEvaluations[versionID]; exists {
		return
	}
	ledger.pendingEvaluations[versionID] = struct{}{}
	ledger.evaluationIDs = append(ledger.evaluationIDs, versionID)
}

func cloneVersion(version Version) Version {
	version.Dependencies = slices.Clone(version.Dependencies)
	return version
}

func versionsEqual(left, right Version) bool {
	return left.ID == right.ID &&
		left.ChangeID == right.ChangeID &&
		left.BaseIntent == right.BaseIntent &&
		left.Content == right.Content &&
		left.Producer == right.Producer &&
		slices.Equal(left.Dependencies, right.Dependencies)
}

func (ledger *transientLedger) PreparePromotion(_ context.Context, prepared PreparedPromotion) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.prepared[prepared.Promotion.ID]; found {
		if existing == prepared {
			return nil
		}
		return errors.New("promotion id is already prepared differently")
	}
	if ledger.pending != "" && ledger.pending != prepared.Promotion.ID {
		return ErrPromotionPending
	}
	version, found := ledger.versions[prepared.Promotion.VersionID]
	if !found {
		return ErrVersionNotFound
	}
	if evaluation, found := ledger.evaluations[version.ID]; found {
		if evaluation.GoverningIntent != ledger.current.ID {
			return ErrIntentAdvanced
		}
		if len(unresolvedRequirements(evaluation, ledger.requirementResponses[version.ID])) > 0 {
			return ErrRequirementRequired
		}
	}
	ids := ledger.versionIDs[version.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != version.ID {
		return ErrVersionAdvanced
	}
	if _, completed := ledger.completed[version.ID]; completed {
		return ErrVersionPromotionStarted
	}
	if prepared.Promotion.FromIntent != ledger.current.ID ||
		prepared.Promotion.ToIntent != prepared.Intent.ID ||
		prepared.Intent.PreviousID != ledger.current.ID ||
		prepared.Intent.Content != version.Content {
		return errors.New("prepared promotion does not advance current intent to its version content")
	}
	if _, found := ledger.revisions[prepared.Intent.ID]; found {
		return errors.New("duplicate intent revision id")
	}
	ledger.prepared[prepared.Promotion.ID] = prepared
	ledger.pending = prepared.Promotion.ID
	delete(ledger.pendingEvaluations, prepared.Promotion.VersionID)
	return nil
}

func (ledger *transientLedger) CompletePromotion(_ context.Context, promotionID PromotionID) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	prepared, found := ledger.prepared[promotionID]
	if !found {
		if _, completed := ledger.promotions[promotionID]; completed {
			return nil
		}
		return errors.New("prepared promotion not found")
	}
	ledger.promotions[promotionID] = prepared.Promotion
	ledger.revisions[prepared.Intent.ID] = prepared.Intent
	ledger.completed[prepared.Promotion.VersionID] = promotionID
	ledger.byIntent[prepared.Promotion.ToIntent] = promotionID
	ledger.current = prepared.Intent
	delete(ledger.pendingEvaluations, prepared.Promotion.VersionID)
	delete(ledger.prepared, promotionID)
	ledger.pending = ""
	return nil
}
