package ledgerfs

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/principal"
)

func (ledger *Ledger) RequirementResponseByIdempotencyKey(ctx context.Context, key string) (intent.RequirementResponse, bool, error) {
	if err := ctx.Err(); err != nil {
		return intent.RequirementResponse{}, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return intent.RequirementResponse{}, false, errors.New("journal is closed")
	}
	record, found := ledger.state.idempotency[key]
	if !found {
		return intent.RequirementResponse{}, false, nil
	}
	if record.operation != requirementResponseOperation {
		return intent.RequirementResponse{}, false, intent.ErrIdempotencyConflict
	}
	response, found := ledger.state.requirementResponseByID[record.requirementID]
	return response, found, nil
}

func (ledger *Ledger) RequirementResponses(ctx context.Context, versionID intent.VersionID) ([]intent.RequirementResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, errors.New("journal is closed")
	}
	return slices.Clone(ledger.state.requirementResponses[versionID]), nil
}

func (ledger *Ledger) PendingRequirements(ctx context.Context, assignee string, after intent.RequirementCursor, limit int) ([]intent.Requirement, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return nil, false, errors.New("journal is closed")
	}
	return pendingRequirements(ledger.state, assignee, after, limit)
}

func (ledger *Ledger) RecordRequirementResponse(ctx context.Context, key string, response intent.RequirementResponse) error {
	copy := response
	return ledger.append(ctx, journalRecord{
		Format:              journalFormat,
		Kind:                requirementResponseRecorded,
		IdempotencyKey:      key,
		RequirementResponse: &copy,
	})
}

func validateRequirementResponse(state *journalState, record journalRecord) error {
	if record.IdempotencyKey == "" || record.RequirementResponse == nil {
		return errors.New("invalid requirement response record")
	}
	response := *record.RequirementResponse
	if existing, found := state.idempotency[record.IdempotencyKey]; found {
		if existing.operation == requirementResponseOperation && state.requirementResponseByID[existing.requirementID] == response {
			return nil
		}
		return intent.ErrIdempotencyConflict
	}
	if response.ID == "" || response.VersionID == "" || strings.TrimSpace(response.Policy) == "" || strings.TrimSpace(response.Rationale) == "" {
		return errors.New("invalid requirement response identity")
	}
	if assignee, valid := principal.Canonical(response.Assignee); !valid || assignee != response.Assignee {
		return errors.New("invalid requirement response assignee")
	}
	if response.Decision != intent.RequirementSatisfied && response.Decision != intent.RequirementRevisionRequested {
		return errors.New("invalid requirement response decision")
	}
	if _, exists := state.requirementResponseByID[response.ID]; exists {
		return errors.New("duplicate requirement response id")
	}
	version, found := state.versions[response.VersionID]
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
	evaluation, found := state.evaluations[version.ID]
	if !found {
		return intent.ErrRequirementNotFound
	}
	if evaluation.GoverningIntent != state.current.ID {
		return intent.ErrIntentAdvanced
	}
	for _, evaluation := range evaluation.PolicyEvaluations {
		if evaluation.Policy != response.Policy || !evaluation.RequiresAction {
			continue
		}
		if evaluation.Assignee != response.Assignee {
			return intent.ErrRequirementNotAssigned
		}
		return nil
	}
	return intent.ErrRequirementNotFound
}

func pendingRequirements(state journalState, assignee string, after intent.RequirementCursor, limit int) ([]intent.Requirement, bool, error) {
	afterSet := after.VersionID != "" || after.Policy != ""
	if afterSet && (after.VersionID == "" || after.Policy == "") {
		return nil, false, intent.ErrRequirementNotFound
	}
	afterFound := !afterSet
	result := make([]intent.Requirement, 0, limit)
	for _, versionID := range state.evaluationIDs {
		evaluation, found := state.evaluations[versionID]
		if !found {
			continue
		}
		_, stillPending := state.pendingEvaluations[versionID]
		visible := stillPending && evaluation.GoverningIntent == state.current.ID
		unresolved := unresolvedRequirements(evaluation, state.requirementResponses[versionID])
		unresolvedByPolicy := make(map[string]intent.Requirement, len(unresolved))
		for _, requirement := range unresolved {
			unresolvedByPolicy[requirement.Policy] = requirement
		}
		for _, evaluated := range evaluation.Requirements() {
			if evaluated.Assignee != assignee {
				continue
			}
			if !afterFound {
				if evaluated.VersionID == after.VersionID && evaluated.Policy == after.Policy {
					afterFound = true
				}
				continue
			}
			if !visible {
				continue
			}
			requirement, open := unresolvedByPolicy[evaluated.Policy]
			if !open {
				continue
			}
			if len(result) == limit {
				return cloneRequirements(result), true, nil
			}
			result = append(result, requirement)
		}
	}
	if !afterFound {
		return nil, false, intent.ErrRequirementNotFound
	}
	return cloneRequirements(result), false, nil
}

func unresolvedRequirements(evaluation intent.Evaluation, responses []intent.RequirementResponse) []intent.Requirement {
	latest := make(map[string]intent.RequirementResponse, len(responses))
	for _, response := range responses {
		latest[response.Policy] = response
	}
	var result []intent.Requirement
	for _, requirement := range evaluation.Requirements() {
		response, found := latest[requirement.Policy]
		if found && response.Decision == intent.RequirementSatisfied {
			continue
		}
		if found {
			copy := response
			requirement.LatestResponse = &copy
		}
		result = append(result, requirement)
	}
	return result
}

func cloneRequirements(requirements []intent.Requirement) []intent.Requirement {
	result := slices.Clone(requirements)
	for index := range result {
		result[index].Evidence = slices.Clone(result[index].Evidence)
		if result[index].LatestResponse != nil {
			copy := *result[index].LatestResponse
			result[index].LatestResponse = &copy
		}
	}
	return result
}
