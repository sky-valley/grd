package ledgerfs

import (
	"context"
	"errors"
	"slices"

	"github.com/sky-valley/grd/internal/intent"
)

func (ledger *Ledger) Evaluation(ctx context.Context, versionID intent.VersionID) (intent.Evaluation, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.Evaluation{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.Evaluation{}, false, errors.New("journal is closed")
	}
	evaluation, found := ledger.state.evaluations[versionID]
	return cloneEvaluation(evaluation), found, nil
}

func (ledger *Ledger) RunnableEvaluations(ctx context.Context, after intent.VersionID, limit int) ([]intent.Version, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	start := 0
	if after != "" {
		start = slices.Index(ledger.state.evaluationIDs, after)
		if start < 0 {
			return nil, false, intent.ErrVersionNotFound
		}
		start++
	}
	versions := make([]intent.Version, 0, limit)
	index := start
	for ; index < len(ledger.state.evaluationIDs) && len(versions) < limit; index++ {
		id := ledger.state.evaluationIDs[index]
		if runnableEvaluation(ledger.state, id) {
			versions = append(versions, cloneVersion(ledger.state.versions[id]))
		}
	}
	for ; index < len(ledger.state.evaluationIDs); index++ {
		if runnableEvaluation(ledger.state, ledger.state.evaluationIDs[index]) {
			return versions, true, nil
		}
	}
	return versions, false, nil
}

func (ledger *Ledger) RecordEvaluation(ctx context.Context, evaluation intent.Evaluation) error {
	copy := cloneEvaluation(evaluation)
	return ledger.append(ctx, journalRecord{
		Format:     journalFormat,
		Kind:       evaluationRecorded,
		Evaluation: &copy,
	})
}

func validateEvaluation(state *journalState, record journalRecord) error {
	if record.Evaluation == nil {
		return errors.New("invalid evaluation record")
	}
	evaluation := *record.Evaluation
	if existing, found := state.evaluations[evaluation.VersionID]; found {
		if sameEvaluation(existing, evaluation) {
			return nil
		}
		return intent.ErrEvaluationAlreadyRecorded
	}
	if err := intent.ValidateEvaluation(evaluation); err != nil {
		return err
	}
	version, found := state.versions[evaluation.VersionID]
	if !found {
		return intent.ErrVersionNotFound
	}
	ids := state.versionIDs[version.ChangeID]
	if len(ids) == 0 || ids[len(ids)-1] != version.ID {
		return intent.ErrVersionAdvanced
	}
	if _, found := state.pendingEvaluations[version.ID]; !found {
		return intent.ErrVersionPromotionStarted
	}
	if evaluation.GoverningIntent != version.BaseIntent {
		return errors.New("evaluation governing Intent does not match the Version base")
	}
	if _, found := state.revisions[evaluation.GoverningIntent]; !found {
		return intent.ErrIntentNotFound
	}
	return nil
}

func runnableEvaluation(state journalState, versionID intent.VersionID) bool {
	if _, found := state.pendingEvaluations[versionID]; !found {
		return false
	}
	evaluation, evaluated := state.evaluations[versionID]
	return !evaluated || evaluation.GoverningIntent == state.current.ID && len(unresolvedRequirements(evaluation, state.requirementResponses[versionID])) == 0
}

func sameEvaluation(left, right intent.Evaluation) bool {
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

func cloneEvaluation(evaluation intent.Evaluation) intent.Evaluation {
	evaluation.PolicyEvaluations = slices.Clone(evaluation.PolicyEvaluations)
	for index := range evaluation.PolicyEvaluations {
		evaluation.PolicyEvaluations[index].Evidence = slices.Clone(evaluation.PolicyEvaluations[index].Evidence)
	}
	return evaluation
}
