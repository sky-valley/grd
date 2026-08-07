package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sky-valley/grd/internal/principal"
)

type PolicyEvaluation struct {
	Policy         string
	Instruction    string
	Assignee       string
	Provenance     EvaluatorProvenance
	RequiresAction bool
	Reason         string
	Evidence       []string
}

type EvaluatorProvenance struct {
	Evaluator        string
	ContractRevision string
}

type Evaluation struct {
	VersionID         VersionID
	GoverningIntent   RevisionID
	PolicyEvaluations []PolicyEvaluation
}

type Requirement struct {
	VersionID      VersionID
	Policy         string
	Assignee       string
	Reason         string
	Evidence       []string
	LatestResponse *RequirementResponse
}

type EvaluationContext struct {
	Version         Version
	GoverningIntent Revision
}

func (record Evaluation) Requirements() []Requirement {
	requirements := make([]Requirement, 0, len(record.PolicyEvaluations))
	for _, policyEvaluation := range record.PolicyEvaluations {
		if !policyEvaluation.RequiresAction {
			continue
		}
		requirements = append(requirements, Requirement{
			VersionID: record.VersionID,
			Policy:    policyEvaluation.Policy,
			Assignee:  policyEvaluation.Assignee,
			Reason:    policyEvaluation.Reason,
			Evidence:  slices.Clone(policyEvaluation.Evidence),
		})
	}
	return requirements
}

func (repository *Repository) Evaluation(ctx context.Context, versionID VersionID) (Evaluation, bool, error) {
	if versionID == "" {
		return Evaluation{}, false, errors.New("evaluation version id is required")
	}
	evaluation, found, err := repository.evaluations.Evaluation(ctx, versionID)
	if err != nil {
		return Evaluation{}, false, fmt.Errorf("read Version evaluation: %w", err)
	}
	return evaluation, found, nil
}

func (repository *Repository) EvaluationContext(ctx context.Context, versionID VersionID) (EvaluationContext, error) {
	if versionID == "" {
		return EvaluationContext{}, errors.New("evaluation version id is required")
	}
	version, found, err := repository.changes.Version(ctx, versionID)
	if err != nil {
		return EvaluationContext{}, fmt.Errorf("read evaluated Version: %w", err)
	}
	if !found {
		return EvaluationContext{}, ErrVersionNotFound
	}
	latest, found, err := repository.changes.LatestVersion(ctx, version.ChangeID)
	if err != nil {
		return EvaluationContext{}, fmt.Errorf("read latest evaluated Version: %w", err)
	}
	if !found || latest.ID != version.ID {
		return EvaluationContext{}, ErrVersionAdvanced
	}
	governing, found, err := repository.intents.Revision(ctx, version.BaseIntent)
	if err != nil {
		return EvaluationContext{}, fmt.Errorf("read governing Intent: %w", err)
	}
	if !found {
		return EvaluationContext{}, ErrIntentNotFound
	}
	return EvaluationContext{Version: cloneVersion(version), GoverningIntent: governing}, nil
}

func (repository *Repository) RecordEvaluation(ctx context.Context, evaluation Evaluation) (Evaluation, error) {
	if err := ValidateEvaluation(evaluation); err != nil {
		return Evaluation{}, err
	}
	if err := repository.evaluations.RecordEvaluation(ctx, evaluation); err != nil {
		return Evaluation{}, fmt.Errorf("record Version evaluation: %w", err)
	}
	return cloneEvaluation(evaluation), nil
}

func (repository *Repository) RunnableEvaluations(ctx context.Context, query PendingEvaluationQuery) (PendingEvaluationPage, error) {
	if query.Limit < 1 || query.Limit > 100 {
		return PendingEvaluationPage{}, errors.New("runnable evaluation page limit must be between 1 and 100")
	}
	versions, more, err := repository.evaluations.RunnableEvaluations(ctx, query.After, query.Limit)
	if err != nil {
		return PendingEvaluationPage{}, fmt.Errorf("read runnable evaluations: %w", err)
	}
	page := PendingEvaluationPage{Versions: versions}
	if more && len(versions) > 0 {
		page.NextCursor = versions[len(versions)-1].ID
	}
	return page, nil
}

func (ledger *transientLedger) Evaluation(_ context.Context, versionID VersionID) (Evaluation, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	evaluation, found := ledger.evaluations[versionID]
	return cloneEvaluation(evaluation), found, nil
}

func (ledger *transientLedger) RunnableEvaluations(_ context.Context, after VersionID, limit int) ([]Version, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	return runnableEvaluations(
		ledger.evaluationIDs,
		ledger.pendingEvaluations,
		ledger.evaluations,
		ledger.requirementResponses,
		ledger.versions,
		ledger.current.ID,
		after,
		limit,
	)
}

func (ledger *transientLedger) RecordEvaluation(_ context.Context, evaluation Evaluation) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.evaluations[evaluation.VersionID]; found {
		if evaluationsEqual(existing, evaluation) {
			return nil
		}
		return ErrEvaluationAlreadyRecorded
	}
	if err := validateEvaluationState(
		ledger.revisions,
		ledger.versions,
		ledger.versionIDs,
		ledger.pendingEvaluations,
		evaluation,
	); err != nil {
		return err
	}
	ledger.evaluations[evaluation.VersionID] = cloneEvaluation(evaluation)
	return nil
}

// ValidateEvaluation checks the storage-independent shape and identity
// invariants of an Evaluation.
func ValidateEvaluation(evaluation Evaluation) error {
	if evaluation.VersionID == "" || evaluation.GoverningIntent == "" || len(evaluation.PolicyEvaluations) == 0 {
		return errors.New("evaluation requires Version, governing Intent, and evaluations")
	}
	policies := make(map[string]struct{}, len(evaluation.PolicyEvaluations))
	for _, evaluation := range evaluation.PolicyEvaluations {
		if strings.TrimSpace(evaluation.Policy) == "" ||
			strings.TrimSpace(evaluation.Instruction) == "" ||
			strings.TrimSpace(evaluation.Assignee) == "" ||
			strings.TrimSpace(evaluation.Reason) == "" ||
			len(evaluation.Evidence) == 0 {
			return errors.New("evaluation requires policy, instruction, assignee, reason, and evidence")
		}
		if evaluation.Policy != strings.TrimSpace(evaluation.Policy) {
			return errors.New("evaluation policy identity must be canonical")
		}
		if strings.ContainsAny(evaluation.Instruction, "\r\n") {
			return errors.New("evaluation instruction must be one line")
		}
		if assignee, valid := principal.Canonical(evaluation.Assignee); !valid || assignee != evaluation.Assignee {
			return errors.New("evaluation assignee must be a canonical principal subject")
		}
		if err := validateEvaluatorProvenance(evaluation.Provenance); err != nil {
			return err
		}
		if _, duplicate := policies[evaluation.Policy]; duplicate {
			return errors.New("evaluation policies must be unique")
		}
		policies[evaluation.Policy] = struct{}{}
		for _, evidence := range evaluation.Evidence {
			if strings.TrimSpace(evidence) == "" {
				return errors.New("evaluation evidence must not be empty")
			}
		}
	}
	return nil
}

func validateEvaluatorProvenance(provenance EvaluatorProvenance) error {
	if provenance == (EvaluatorProvenance{}) {
		return nil
	}
	if provenance.Evaluator == "" || provenance.ContractRevision == "" ||
		provenance.Evaluator != strings.TrimSpace(provenance.Evaluator) ||
		provenance.ContractRevision != strings.TrimSpace(provenance.ContractRevision) ||
		strings.ContainsAny(provenance.Evaluator+provenance.ContractRevision, "\r\n") {
		return errors.New("evaluation evaluator provenance requires canonical evaluator and contract revision")
	}
	if len(provenance.Evaluator) > 256 || len(provenance.ContractRevision) > 128 {
		return errors.New("evaluation evaluator provenance is too large")
	}
	return nil
}

func validateEvaluationState(
	revisions map[RevisionID]Revision,
	versions map[VersionID]Version,
	versionIDs map[ChangeID][]VersionID,
	pending map[VersionID]struct{},
	evaluation Evaluation,
) error {
	if err := ValidateEvaluation(evaluation); err != nil {
		return err
	}
	version, found := versions[evaluation.VersionID]
	if !found {
		return ErrVersionNotFound
	}
	ids := versionIDs[version.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != version.ID {
		return ErrVersionAdvanced
	}
	if _, found := pending[version.ID]; !found {
		return ErrVersionPromotionStarted
	}
	if evaluation.GoverningIntent != version.BaseIntent {
		return errors.New("evaluation governing Intent does not match the Version base")
	}
	if _, found := revisions[evaluation.GoverningIntent]; !found {
		return ErrIntentNotFound
	}
	return nil
}

func runnableEvaluations(
	ordered []VersionID,
	pending map[VersionID]struct{},
	evaluations map[VersionID]Evaluation,
	requirementResponses map[VersionID][]RequirementResponse,
	versions map[VersionID]Version,
	currentIntent RevisionID,
	after VersionID,
	limit int,
) ([]Version, bool, error) {
	start := 0
	if after != "" {
		start = slices.Index(ordered, after)
		if start < 0 {
			return nil, false, ErrVersionNotFound
		}
		start++
	}
	result := make([]Version, 0, limit)
	index := start
	for ; index < len(ordered) && len(result) < limit; index++ {
		id := ordered[index]
		if runnableEvaluation(id, pending, evaluations, requirementResponses, currentIntent) {
			result = append(result, cloneVersion(versions[id]))
		}
	}
	for ; index < len(ordered); index++ {
		if runnableEvaluation(ordered[index], pending, evaluations, requirementResponses, currentIntent) {
			return result, true, nil
		}
	}
	return result, false, nil
}

func runnableEvaluation(versionID VersionID, pending map[VersionID]struct{}, evaluations map[VersionID]Evaluation, requirementResponses map[VersionID][]RequirementResponse, currentIntent RevisionID) bool {
	if _, found := pending[versionID]; !found {
		return false
	}
	evaluation, evaluated := evaluations[versionID]
	return !evaluated || evaluation.GoverningIntent == currentIntent && len(unresolvedRequirements(evaluation, requirementResponses[versionID])) == 0
}

func cloneEvaluation(evaluation Evaluation) Evaluation {
	evaluation.PolicyEvaluations = slices.Clone(evaluation.PolicyEvaluations)
	for index := range evaluation.PolicyEvaluations {
		evaluation.PolicyEvaluations[index].Evidence = slices.Clone(evaluation.PolicyEvaluations[index].Evidence)
	}
	return evaluation
}

func evaluationsEqual(left, right Evaluation) bool {
	if left.VersionID != right.VersionID || left.GoverningIntent != right.GoverningIntent || len(left.PolicyEvaluations) != len(right.PolicyEvaluations) {
		return false
	}
	for index := range left.PolicyEvaluations {
		l, r := left.PolicyEvaluations[index], right.PolicyEvaluations[index]
		if l.Policy != r.Policy || l.Instruction != r.Instruction || l.Assignee != r.Assignee || l.Provenance != r.Provenance || l.RequiresAction != r.RequiresAction || l.Reason != r.Reason || !slices.Equal(l.Evidence, r.Evidence) {
			return false
		}
	}
	return true
}
