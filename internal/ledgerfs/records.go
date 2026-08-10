package ledgerfs

import (
	"errors"
	"fmt"
	"slices"

	"github.com/sky-valley/grd/internal/intent"
)

const (
	repositoryInitialized            = "repository_initialized"
	proposalRecorded                 = "proposal_recorded"
	amendmentRecorded                = "amendment_recorded"
	dependentReconciliationRecorded  = "dependent_reconciliation_recorded"
	heldVersionRebaseRecorded        = "held_version_rebase_recorded"
	promotionPrepared                = "promotion_prepared"
	promotionCompleted               = "promotion_completed"
	reconciliationConflictRecorded   = "reconciliation_conflict_recorded"
	reconciliationResolutionRecorded = "reconciliation_resolution_recorded"
	evaluationRecorded               = "policy_evaluation_recorded"
	requirementResponseRecorded      = "requirement_response_recorded"
)

type journalState struct {
	current                 intent.Revision
	revisions               map[intent.RevisionID]intent.Revision
	changes                 map[intent.ChangeID]intent.Change
	versions                map[intent.VersionID]intent.Version
	versionIDs              map[intent.ChangeID][]intent.VersionID
	dependents              map[intent.VersionID][]intent.VersionID
	pendingEvaluations      map[intent.VersionID]struct{}
	evaluationIDs           []intent.VersionID
	evaluations             map[intent.VersionID]intent.Evaluation
	requirementResponses    map[intent.VersionID][]intent.RequirementResponse
	requirementResponseByID map[intent.RequirementResponseID]intent.RequirementResponse
	amendments              map[intent.VersionID]intent.Amendment
	reconciliations         map[intent.VersionID]intent.DependentReconciliation
	reconciliationIDs       []intent.VersionID
	rebases                 map[intent.VersionID]intent.HeldVersionRebase
	promotions              map[intent.PromotionID]intent.Promotion
	prepared                map[intent.PromotionID]intent.PreparedPromotion
	pending                 intent.PromotionID
	completed               map[intent.VersionID]intent.PromotionID
	byIntent                map[intent.RevisionID]intent.PromotionID
	conflicts               map[intent.ConflictID]intent.ReconciliationConflict
	conflictIDs             []intent.ConflictID
	resolutions             map[intent.ConflictID]intent.ReconciliationResolution
	history                 []intent.HistoryFact
	journalCursor           intent.HistoryCursor
	idempotency             map[string]idempotencyRecord
}

type idempotencyOperation uint8

const (
	proposalOperation idempotencyOperation = iota + 1
	amendmentOperation
	dependentReconciliationOperation
	heldVersionRebaseOperation
	reconciliationConflictOperation
	reconciliationResolutionOperation
	requirementResponseOperation
)

type idempotencyRecord struct {
	operation     idempotencyOperation
	versionID     intent.VersionID
	conflictID    intent.ConflictID
	requirementID intent.RequirementResponseID
}

type journalRecord struct {
	Format                   int                              `json:"format"`
	Kind                     string                           `json:"kind"`
	Initial                  *intent.Revision                 `json:"initial,omitempty"`
	IdempotencyKey           string                           `json:"idempotency_key,omitempty"`
	Change                   *intent.Change                   `json:"change,omitempty"`
	Version                  *intent.Version                  `json:"version,omitempty"`
	Amendment                *intent.Amendment                `json:"amendment,omitempty"`
	DependentReconciliation  *intent.DependentReconciliation  `json:"dependent_reconciliation,omitempty"`
	HeldVersionRebase        *intent.HeldVersionRebase        `json:"held_version_rebase,omitempty"`
	Promotion                *intent.Promotion                `json:"promotion,omitempty"`
	PromotionID              intent.PromotionID               `json:"promotion_id,omitempty"`
	NextIntent               *intent.Revision                 `json:"next_intent,omitempty"`
	ReconciliationConflict   *intent.ReconciliationConflict   `json:"reconciliation_conflict,omitempty"`
	ReconciliationResolution *intent.ReconciliationResolution `json:"reconciliation_resolution,omitempty"`
	Evaluation               *intent.Evaluation               `json:"policy_evaluation,omitempty"`
	RequirementResponse      *intent.RequirementResponse      `json:"requirement_response,omitempty"`
}

func validateRecord(state *journalState, record journalRecord) error {
	if record.Format != journalFormat {
		return fmt.Errorf("unsupported journal format %d", record.Format)
	}
	switch record.Kind {
	case repositoryInitialized:
		return validateInitialization(state, record)
	case proposalRecorded:
		return validateProposal(state, record)
	case amendmentRecorded:
		return validateAmendment(state, record)
	case dependentReconciliationRecorded:
		return validateDependentReconciliation(state, record)
	case heldVersionRebaseRecorded:
		return validateHeldVersionRebase(state, record)
	case promotionPrepared:
		return validatePreparedPromotion(state, record)
	case promotionCompleted:
		return validateCompletedPromotion(state, record)
	case reconciliationConflictRecorded:
		return validateReconciliationConflict(state, record)
	case reconciliationResolutionRecorded:
		return validateReconciliationResolution(state, record)
	case evaluationRecorded:
		return validateEvaluation(state, record)
	case requirementResponseRecorded:
		return validateRequirementResponse(state, record)
	default:
		return fmt.Errorf("unknown journal record kind %q", record.Kind)
	}
}

func recordAlreadyApplied(state *journalState, record journalRecord) bool {
	switch record.Kind {
	case evaluationRecorded:
		if record.Evaluation == nil {
			return false
		}
		_, found := state.evaluations[record.Evaluation.VersionID]
		return found
	case requirementResponseRecorded:
		if record.RequirementResponse == nil {
			return false
		}
		_, found := state.requirementResponseByID[record.RequirementResponse.ID]
		return found
	default:
		return false
	}
}

func validateReconciliationConflict(state *journalState, record journalRecord) error {
	if state.current.ID == "" {
		return errors.New("reconciliation conflict precedes repository initialization")
	}
	if record.IdempotencyKey == "" || record.ReconciliationConflict == nil {
		return errors.New("invalid reconciliation conflict record")
	}
	conflict := *record.ReconciliationConflict
	if existing, ok := state.idempotency[record.IdempotencyKey]; ok {
		if existing.operation != reconciliationConflictOperation {
			return intent.ErrIdempotencyConflict
		}
		stored, found := state.conflicts[existing.conflictID]
		if found && sameReconciliationConflict(stored, conflict) {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	if conflict.ID == "" || conflict.Change.ID == "" || conflict.Version.ID == "" ||
		conflict.Version.ChangeID != conflict.Change.ID ||
		conflict.FromVersion == "" || conflict.ToVersion == "" || conflict.BaseIntent == "" || conflict.ReportedBy == "" {
		return errors.New("invalid reconciliation conflict identity")
	}
	from, fromFound := state.versions[conflict.FromVersion]
	to, toFound := state.versions[conflict.ToVersion]
	if !fromFound || !toFound || from.ChangeID != to.ChangeID ||
		!recordedAmendmentDescendant(state, from.ID, to.ID) {
		return errors.New("invalid reconciliation conflict lineage")
	}
	promotionID, promoted := state.completed[to.ID]
	if !promoted {
		return intent.ErrVersionNotPromoted
	}
	promotion, found := state.promotions[promotionID]
	if !found || conflict.BaseIntent != state.current.ID ||
		!recordedRevisionDescendsFrom(state, state.current.ID, promotion.ToIntent) {
		return intent.ErrIntentAdvanced
	}
	descendant, descendantFound := state.versions[conflict.Version.ID]
	descendantChange, changeFound := state.changes[conflict.Change.ID]
	descendantIDs := state.versionIDs[conflict.Change.ID]
	if !descendantFound || !changeFound ||
		len(descendantIDs) == 0 || descendantIDs[len(descendantIDs)-1] != descendant.ID ||
		descendant.ChangeID == from.ChangeID ||
		descendant.BaseIntent != from.BaseIntent ||
		descendant.ChangeID != descendantChange.ID ||
		descendantChange != conflict.Change ||
		!sameVersion(descendant, conflict.Version) {
		return errors.New("invalid reconciliation descendant version")
	}
	if _, found := state.conflicts[conflict.ID]; found {
		return errors.New("duplicate reconciliation conflict id")
	}
	paths, err := intent.NormalizeReconciliationConflictPaths(conflict.AffectedPaths)
	if err != nil || !slices.Equal(paths, conflict.AffectedPaths) {
		return errors.New("invalid reconciliation conflict diagnostics")
	}
	return nil
}

func validateAmendment(state *journalState, record journalRecord) error {
	if state.current.ID == "" {
		return errors.New("amendment precedes repository initialization")
	}
	if record.IdempotencyKey == "" || record.Version == nil || record.Amendment == nil {
		return errors.New("invalid amendment record")
	}
	if existing, ok := state.idempotency[record.IdempotencyKey]; ok {
		if existing.operation != amendmentOperation {
			return intent.ErrIdempotencyConflict
		}
		existingVersionID := existing.versionID
		existingVersion, versionFound := state.versions[existingVersionID]
		existingAmendment, amendmentFound := state.amendments[existingVersionID]
		if versionFound && amendmentFound && sameVersion(existingVersion, *record.Version) && existingAmendment == *record.Amendment {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	previous, found := state.versions[record.Amendment.FromVersion]
	if !found {
		return errors.New("amendment source version is not recorded")
	}
	ids := state.versionIDs[previous.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != previous.ID {
		return intent.ErrVersionAdvanced
	}
	if _, promoted := state.completed[previous.ID]; promoted {
		return intent.ErrVersionPromotionStarted
	}
	for _, prepared := range state.prepared {
		if prepared.Promotion.VersionID == previous.ID {
			return intent.ErrVersionPromotionStarted
		}
	}
	version := *record.Version
	if version.ID == "" || version.ChangeID != previous.ChangeID || record.Amendment.ToVersion != version.ID || record.Amendment.Rationale == "" {
		return errors.New("invalid amendment identity")
	}
	if version.BaseIntent != previous.BaseIntent || !slices.Equal(version.Dependencies, previous.Dependencies) {
		return errors.New("amendment does not preserve version base and dependencies")
	}
	if version.Content.Engine == "" || version.Content.Revision == "" || version.Producer == "" {
		return errors.New("invalid amended version")
	}
	if _, found := state.versions[version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}

func validateInitialization(state *journalState, record journalRecord) error {
	if record.Initial == nil || record.Initial.ID == "" || record.Initial.PreviousID != "" || record.Initial.Content.Engine == "" || record.Initial.Content.Revision == "" {
		return errors.New("invalid repository initialization")
	}
	if state.current.ID != "" {
		if state.current == *record.Initial && len(state.revisions) == 1 {
			return nil
		}
		return errors.New("repository is already initialized")
	}
	return nil
}

func validateProposal(state *journalState, record journalRecord) error {
	if state.current.ID == "" {
		return errors.New("proposal precedes repository initialization")
	}
	if record.IdempotencyKey == "" || record.Change == nil || record.Version == nil {
		return errors.New("invalid proposal record")
	}
	if existing, ok := state.idempotency[record.IdempotencyKey]; ok {
		if existing.operation != proposalOperation {
			return intent.ErrIdempotencyConflict
		}
		existingVersionID := existing.versionID
		existingVersion, versionFound := state.versions[existingVersionID]
		existingChange, changeFound := state.changes[existingVersion.ChangeID]
		if versionFound && changeFound && sameVersion(existingVersion, *record.Version) && existingChange == *record.Change {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	if record.Change.ID == "" || record.Version.ID == "" || record.Version.ChangeID != record.Change.ID {
		return errors.New("invalid proposal identity")
	}
	if _, found := state.revisions[record.Version.BaseIntent]; !found {
		return errors.New("proposal base intent is not recorded")
	}
	if record.Version.Content.Engine == "" || record.Version.Content.Revision == "" || record.Version.Producer == "" {
		return errors.New("invalid proposal version")
	}
	seenDependencies := make(map[intent.VersionID]struct{}, len(record.Version.Dependencies))
	for _, dependencyID := range record.Version.Dependencies {
		if dependencyID == "" {
			return errors.New("invalid proposal dependency")
		}
		if _, duplicate := seenDependencies[dependencyID]; duplicate {
			return errors.New("duplicate proposal dependency")
		}
		seenDependencies[dependencyID] = struct{}{}
		if _, found := state.versions[dependencyID]; !found {
			return errors.New("proposal dependency is not recorded")
		}
	}
	if _, found := state.changes[record.Change.ID]; found {
		return errors.New("duplicate change id")
	}
	if _, found := state.versions[record.Version.ID]; found {
		return errors.New("duplicate version id")
	}
	return nil
}

func sameVersion(left, right intent.Version) bool {
	return left.ID == right.ID &&
		left.ChangeID == right.ChangeID &&
		left.BaseIntent == right.BaseIntent &&
		left.Content == right.Content &&
		left.Producer == right.Producer &&
		slices.Equal(left.Dependencies, right.Dependencies)
}

func cloneVersion(version intent.Version) intent.Version {
	version.Dependencies = slices.Clone(version.Dependencies)
	return version
}

func validatePreparedPromotion(state *journalState, record journalRecord) error {
	if record.Promotion == nil || record.NextIntent == nil {
		return errors.New("invalid prepared promotion record")
	}
	prepared := intent.PreparedPromotion{Promotion: *record.Promotion, Intent: *record.NextIntent}
	if existing, found := state.prepared[prepared.Promotion.ID]; found {
		if existing == prepared {
			return nil
		}
		return errors.New("promotion id is already prepared differently")
	}
	if existing, found := state.promotions[prepared.Promotion.ID]; found {
		revision := state.revisions[existing.ToIntent]
		if existing == prepared.Promotion && revision == prepared.Intent {
			return nil
		}
		return errors.New("promotion id is already completed differently")
	}
	if state.pending != "" {
		return intent.ErrPromotionPending
	}
	version, found := state.versions[prepared.Promotion.VersionID]
	if prepared.Promotion.ID == "" || !found {
		return errors.New("prepared promotion references an unknown version")
	}
	if evaluation, found := state.evaluations[version.ID]; found {
		if evaluation.GoverningIntent != state.current.ID {
			return intent.ErrIntentAdvanced
		}
		if len(unresolvedRequirements(evaluation, state.requirementResponses[version.ID])) > 0 {
			return intent.ErrRequirementRequired
		}
	}
	if _, exists := state.completed[version.ID]; exists {
		return intent.ErrVersionPromotionStarted
	}
	ids := state.versionIDs[version.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != version.ID {
		return intent.ErrVersionAdvanced
	}
	if prepared.Promotion.FromIntent != state.current.ID || prepared.Promotion.ToIntent != prepared.Intent.ID || prepared.Intent.PreviousID != state.current.ID || prepared.Intent.Content != version.Content {
		return errors.New("prepared promotion does not advance current intent to its version content")
	}
	if _, found := state.revisions[prepared.Intent.ID]; found {
		return errors.New("duplicate intent revision id")
	}
	return nil
}

func validateCompletedPromotion(state *journalState, record journalRecord) error {
	if record.PromotionID == "" {
		return errors.New("completed promotion id is required")
	}
	if _, found := state.promotions[record.PromotionID]; found {
		return nil
	}
	if _, found := state.prepared[record.PromotionID]; !found {
		return errors.New("prepared promotion not found")
	}
	return nil
}

func newJournalState() journalState {
	return journalState{
		revisions:               make(map[intent.RevisionID]intent.Revision),
		changes:                 make(map[intent.ChangeID]intent.Change),
		versions:                make(map[intent.VersionID]intent.Version),
		versionIDs:              make(map[intent.ChangeID][]intent.VersionID),
		dependents:              make(map[intent.VersionID][]intent.VersionID),
		pendingEvaluations:      make(map[intent.VersionID]struct{}),
		evaluations:             make(map[intent.VersionID]intent.Evaluation),
		requirementResponses:    make(map[intent.VersionID][]intent.RequirementResponse),
		requirementResponseByID: make(map[intent.RequirementResponseID]intent.RequirementResponse),
		amendments:              make(map[intent.VersionID]intent.Amendment),
		reconciliations:         make(map[intent.VersionID]intent.DependentReconciliation),
		rebases:                 make(map[intent.VersionID]intent.HeldVersionRebase),
		promotions:              make(map[intent.PromotionID]intent.Promotion),
		prepared:                make(map[intent.PromotionID]intent.PreparedPromotion),
		completed:               make(map[intent.VersionID]intent.PromotionID),
		byIntent:                make(map[intent.RevisionID]intent.PromotionID),
		conflicts:               make(map[intent.ConflictID]intent.ReconciliationConflict),
		resolutions:             make(map[intent.ConflictID]intent.ReconciliationResolution),
		idempotency:             make(map[string]idempotencyRecord),
	}
}

func applyValidatedRecord(state *journalState, record journalRecord) {
	state.journalCursor++
	switch record.Kind {
	case repositoryInitialized:
		if state.current.ID == "" {
			state.current = *record.Initial
			state.revisions[record.Initial.ID] = *record.Initial
			appendJournalHistory(state, intent.HistoryFact{Kind: intent.HistoryIntentInitialized, Intent: record.Initial})
		}
	case proposalRecorded:
		if _, exists := state.idempotency[record.IdempotencyKey]; !exists {
			state.changes[record.Change.ID] = *record.Change
			state.versions[record.Version.ID] = cloneVersion(*record.Version)
			state.versionIDs[record.Change.ID] = append(state.versionIDs[record.Change.ID], record.Version.ID)
			for _, dependencyID := range record.Version.Dependencies {
				state.dependents[dependencyID] = append(state.dependents[dependencyID], record.Version.ID)
			}
			state.idempotency[record.IdempotencyKey] = idempotencyRecord{operation: proposalOperation, versionID: record.Version.ID}
			beginPendingEvaluation(state, record.Version.ID, "")
			appendJournalHistory(state, intent.HistoryFact{Kind: intent.HistoryVersionProposed, Change: record.Change, Version: record.Version})
		}
	case amendmentRecorded:
		if _, exists := state.idempotency[record.IdempotencyKey]; !exists {
			state.versions[record.Version.ID] = cloneVersion(*record.Version)
			state.versionIDs[record.Version.ChangeID] = append(state.versionIDs[record.Version.ChangeID], record.Version.ID)
			for _, dependencyID := range record.Version.Dependencies {
				state.dependents[dependencyID] = append(state.dependents[dependencyID], record.Version.ID)
			}
			state.amendments[record.Version.ID] = *record.Amendment
			state.idempotency[record.IdempotencyKey] = idempotencyRecord{operation: amendmentOperation, versionID: record.Version.ID}
			beginPendingEvaluation(state, record.Version.ID, record.Amendment.FromVersion)
			appendJournalHistory(state, intent.HistoryFact{Kind: intent.HistoryVersionAmended, Version: record.Version, Amendment: record.Amendment})
		}
	case dependentReconciliationRecorded:
		applyDependentReconciliation(state, record)
	case heldVersionRebaseRecorded:
		applyHeldVersionRebase(state, record)
	case promotionPrepared:
		if _, exists := state.promotions[record.Promotion.ID]; exists {
			return
		}
		if _, exists := state.prepared[record.Promotion.ID]; !exists {
			prepared := intent.PreparedPromotion{Promotion: *record.Promotion, Intent: *record.NextIntent}
			state.prepared[prepared.Promotion.ID] = prepared
			state.pending = prepared.Promotion.ID
			delete(state.pendingEvaluations, prepared.Promotion.VersionID)
		}
	case promotionCompleted:
		if _, exists := state.promotions[record.PromotionID]; !exists {
			prepared := state.prepared[record.PromotionID]
			state.revisions[prepared.Intent.ID] = prepared.Intent
			state.promotions[prepared.Promotion.ID] = prepared.Promotion
			state.completed[prepared.Promotion.VersionID] = prepared.Promotion.ID
			state.byIntent[prepared.Promotion.ToIntent] = prepared.Promotion.ID
			state.current = prepared.Intent
			delete(state.pendingEvaluations, prepared.Promotion.VersionID)
			delete(state.prepared, prepared.Promotion.ID)
			state.pending = ""
			appendJournalHistory(state, intent.HistoryFact{Kind: intent.HistoryVersionPromoted, Intent: &prepared.Intent, Promotion: &prepared.Promotion})
		}
	case reconciliationConflictRecorded:
		if _, exists := state.idempotency[record.IdempotencyKey]; !exists {
			conflict := cloneReconciliationConflict(*record.ReconciliationConflict)
			state.conflicts[conflict.ID] = conflict
			state.conflictIDs = append(state.conflictIDs, conflict.ID)
			state.idempotency[record.IdempotencyKey] = idempotencyRecord{
				operation:  reconciliationConflictOperation,
				versionID:  conflict.Version.ID,
				conflictID: conflict.ID,
			}
			appendJournalHistory(state, intent.HistoryFact{Kind: intent.HistoryConflictRecorded, ReconciliationConflict: &conflict})
		}
	case reconciliationResolutionRecorded:
		applyReconciliationResolution(state, record)
	case evaluationRecorded:
		if _, exists := state.evaluations[record.Evaluation.VersionID]; !exists {
			state.evaluations[record.Evaluation.VersionID] = cloneEvaluation(*record.Evaluation)
			appendJournalHistory(state, intent.HistoryFact{Kind: intent.HistoryEvaluationRecorded, Evaluation: record.Evaluation})
		}
	case requirementResponseRecorded:
		if _, exists := state.idempotency[record.IdempotencyKey]; !exists {
			response := *record.RequirementResponse
			state.requirementResponses[response.VersionID] = append(state.requirementResponses[response.VersionID], response)
			state.requirementResponseByID[response.ID] = response
			state.idempotency[record.IdempotencyKey] = idempotencyRecord{operation: requirementResponseOperation, requirementID: response.ID}
			appendJournalHistory(state, intent.HistoryFact{Kind: intent.HistoryRequirementResponded, RequirementResponse: record.RequirementResponse})
		}
	}
}

func appendJournalHistory(state *journalState, fact intent.HistoryFact) {
	fact.Cursor = state.journalCursor
	state.history = append(state.history, intent.CloneHistoryFacts([]intent.HistoryFact{fact})[0])
}

func beginPendingEvaluation(state *journalState, versionID, superseded intent.VersionID) {
	delete(state.pendingEvaluations, superseded)
	if _, exists := state.pendingEvaluations[versionID]; exists {
		return
	}
	state.pendingEvaluations[versionID] = struct{}{}
	state.evaluationIDs = append(state.evaluationIDs, versionID)
}

func sameReconciliationConflict(left, right intent.ReconciliationConflict) bool {
	return left.ID == right.ID &&
		left.Change == right.Change &&
		left.FromVersion == right.FromVersion &&
		left.ToVersion == right.ToVersion &&
		left.BaseIntent == right.BaseIntent &&
		left.ReportedBy == right.ReportedBy &&
		sameVersion(left.Version, right.Version) &&
		slices.Equal(left.AffectedPaths, right.AffectedPaths)
}

func cloneReconciliationConflict(conflict intent.ReconciliationConflict) intent.ReconciliationConflict {
	conflict.Version = cloneVersion(conflict.Version)
	conflict.AffectedPaths = slices.Clone(conflict.AffectedPaths)
	return conflict
}

func cloneReconciliationConflictInspection(inspection intent.ReconciliationConflictInspection) intent.ReconciliationConflictInspection {
	inspection.ReconciliationConflict = cloneReconciliationConflict(inspection.ReconciliationConflict)
	if inspection.Resolution != nil {
		resolution := *inspection.Resolution
		inspection.Resolution = &resolution
	}
	return inspection
}
