package intent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sky-valley/grd/internal/principal"
)

var ErrRequirementNotAssigned = errors.New("requirement is not assigned to this principal")
var ErrRequirementNotFound = errors.New("requirement not found")

type RequirementResponseID string

type RequirementDecision string

const (
	RequirementSatisfied         RequirementDecision = "satisfied"
	RequirementRevisionRequested RequirementDecision = "revision_requested"
)

type RequirementResponse struct {
	ID        RequirementResponseID
	VersionID VersionID
	Policy    string
	Assignee  string
	Decision  RequirementDecision
	Rationale string
}

type RequirementResponseRequest struct {
	IdempotencyKey string
	VersionID      VersionID
	Policy         string
	Assignee       string
	Decision       RequirementDecision
	Rationale      string
}

type PendingRequirementQuery struct {
	Assignee string
	After    RequirementCursor
	Limit    int
}

type RequirementCursor struct {
	VersionID VersionID
	Policy    string
}

type PendingRequirementPage struct {
	Requirements []Requirement
	NextCursor   RequirementCursor
}

func (repository *Repository) RecordRequirementResponse(ctx context.Context, request RequirementResponseRequest) (RequirementResponse, error) {
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.Policy = strings.TrimSpace(request.Policy)
	request.Assignee = strings.TrimSpace(request.Assignee)
	request.Rationale = strings.TrimSpace(request.Rationale)
	if request.IdempotencyKey == "" || request.VersionID == "" || request.Policy == "" || request.Rationale == "" {
		return RequirementResponse{}, errors.New("requirement response requires idempotency key, Version, policy, and rationale")
	}
	if assignee, valid := principal.Canonical(request.Assignee); !valid || assignee != request.Assignee {
		return RequirementResponse{}, ErrRequirementNotAssigned
	}
	if request.Decision != RequirementSatisfied && request.Decision != RequirementRevisionRequested {
		return RequirementResponse{}, errors.New("requirement response decision must be satisfied or revision_requested")
	}

	repository.changeMu.Lock()
	defer repository.changeMu.Unlock()
	existing, found, err := repository.requirementResponses.RequirementResponseByIdempotencyKey(ctx, request.IdempotencyKey)
	if err != nil {
		return RequirementResponse{}, fmt.Errorf("read requirement response idempotency: %w", err)
	}
	if found {
		if sameRequirementResponseRequest(existing, request) {
			return existing, nil
		}
		return RequirementResponse{}, ErrIdempotencyConflict
	}

	id, err := newID("requirement_response")
	if err != nil {
		return RequirementResponse{}, fmt.Errorf("create requirement response id: %w", err)
	}
	response := RequirementResponse{
		ID:        RequirementResponseID(id),
		VersionID: request.VersionID,
		Policy:    request.Policy,
		Assignee:  request.Assignee,
		Decision:  request.Decision,
		Rationale: request.Rationale,
	}
	if err := repository.requirementResponses.RecordRequirementResponse(ctx, request.IdempotencyKey, response); err != nil {
		return RequirementResponse{}, fmt.Errorf("record requirement response: %w", err)
	}
	return response, nil
}

func (repository *Repository) PendingRequirements(ctx context.Context, query PendingRequirementQuery) (PendingRequirementPage, error) {
	query.Assignee = strings.TrimSpace(query.Assignee)
	if assignee, valid := principal.Canonical(query.Assignee); !valid || assignee != query.Assignee {
		return PendingRequirementPage{}, ErrRequirementNotAssigned
	}
	if query.Limit < 1 || query.Limit > 100 {
		return PendingRequirementPage{}, errors.New("pending requirement page limit must be between 1 and 100")
	}
	requirements, more, err := repository.requirementResponses.PendingRequirements(ctx, query.Assignee, query.After, query.Limit)
	if err != nil {
		return PendingRequirementPage{}, fmt.Errorf("read pending requirements: %w", err)
	}
	page := PendingRequirementPage{Requirements: requirements}
	if more && len(requirements) > 0 {
		last := requirements[len(requirements)-1]
		page.NextCursor = RequirementCursor{VersionID: last.VersionID, Policy: last.Policy}
	}
	return page, nil
}

func (repository *Repository) UnresolvedRequirements(ctx context.Context, versionID VersionID) ([]Requirement, error) {
	evaluation, found, err := repository.evaluations.Evaluation(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("read Version evaluation: %w", err)
	}
	if !found {
		return nil, ErrRequirementNotFound
	}
	responses, err := repository.requirementResponses.RequirementResponses(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("read Version requirement responses: %w", err)
	}
	return cloneRequirements(unresolvedRequirements(evaluation, responses)), nil
}

func unresolvedRequirements(evaluation Evaluation, responses []RequirementResponse) []Requirement {
	latest := make(map[string]RequirementResponse, len(responses))
	for _, response := range responses {
		latest[response.Policy] = response
	}
	requirements := evaluation.Requirements()
	result := make([]Requirement, 0, len(requirements))
	for _, requirement := range requirements {
		response, found := latest[requirement.Policy]
		if found && response.Decision == RequirementSatisfied {
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

func cloneRequirements(requirements []Requirement) []Requirement {
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

func sameRequirementResponseRequest(response RequirementResponse, request RequirementResponseRequest) bool {
	return response.VersionID == request.VersionID &&
		response.Policy == request.Policy &&
		response.Assignee == request.Assignee &&
		response.Decision == request.Decision &&
		response.Rationale == request.Rationale
}

func (ledger *transientLedger) RequirementResponseByIdempotencyKey(_ context.Context, key string) (RequirementResponse, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	record, found := ledger.idempotency[key]
	if !found {
		return RequirementResponse{}, false, nil
	}
	if record.operation != transientRequirementResponseOperation {
		return RequirementResponse{}, false, ErrIdempotencyConflict
	}
	response, found := ledger.requirementResponseByID[record.requirementID]
	return response, found, nil
}

func (ledger *transientLedger) RequirementResponses(_ context.Context, versionID VersionID) ([]RequirementResponse, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	return slices.Clone(ledger.requirementResponses[versionID]), nil
}

func (ledger *transientLedger) PendingRequirements(_ context.Context, assignee string, after RequirementCursor, limit int) ([]Requirement, bool, error) {
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	return pendingRequirements(
		ledger.evaluationIDs,
		ledger.pendingEvaluations,
		ledger.evaluations,
		ledger.requirementResponses,
		ledger.current.ID,
		assignee,
		after,
		limit,
	)
}

func (ledger *transientLedger) RecordRequirementResponse(_ context.Context, key string, response RequirementResponse) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if existing, found := ledger.idempotency[key]; found {
		if existing.operation == transientRequirementResponseOperation && ledger.requirementResponseByID[existing.requirementID] == response {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if err := validateRequirementResponseState(
		ledger.current.ID,
		ledger.versions,
		ledger.versionIDs,
		ledger.pendingEvaluations,
		ledger.evaluations,
		ledger.requirementResponseByID,
		response,
	); err != nil {
		return err
	}
	ledger.requirementResponses[response.VersionID] = append(ledger.requirementResponses[response.VersionID], response)
	ledger.requirementResponseByID[response.ID] = response
	ledger.idempotency[key] = transientIdempotencyRecord{operation: transientRequirementResponseOperation, requirementID: response.ID}
	return nil
}

func validateRequirementResponseState(
	currentIntent RevisionID,
	versions map[VersionID]Version,
	versionIDs map[ChangeID][]VersionID,
	pending map[VersionID]struct{},
	evaluations map[VersionID]Evaluation,
	responses map[RequirementResponseID]RequirementResponse,
	response RequirementResponse,
) error {
	if response.ID == "" || response.VersionID == "" || response.Policy == "" || response.Assignee == "" || response.Rationale == "" {
		return errors.New("requirement response requires identity, Version, policy, assignee, and rationale")
	}
	if response.Decision != RequirementSatisfied && response.Decision != RequirementRevisionRequested {
		return errors.New("invalid requirement response decision")
	}
	if _, exists := responses[response.ID]; exists {
		return errors.New("duplicate requirement response id")
	}
	version, found := versions[response.VersionID]
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
	evaluation, found := evaluations[version.ID]
	if !found {
		return ErrRequirementNotFound
	}
	if evaluation.GoverningIntent != currentIntent {
		return ErrIntentAdvanced
	}
	for _, evaluation := range evaluation.PolicyEvaluations {
		if evaluation.Policy != response.Policy || !evaluation.RequiresAction {
			continue
		}
		if evaluation.Assignee != response.Assignee {
			return ErrRequirementNotAssigned
		}
		return nil
	}
	return ErrRequirementNotFound
}

func pendingRequirements(
	ordered []VersionID,
	pending map[VersionID]struct{},
	evaluations map[VersionID]Evaluation,
	responses map[VersionID][]RequirementResponse,
	currentIntent RevisionID,
	assignee string,
	after RequirementCursor,
	limit int,
) ([]Requirement, bool, error) {
	afterSet := after.VersionID != "" || after.Policy != ""
	if afterSet && (after.VersionID == "" || after.Policy == "") {
		return nil, false, ErrRequirementNotFound
	}
	afterFound := !afterSet
	result := make([]Requirement, 0, limit)
	for _, versionID := range ordered {
		evaluation, found := evaluations[versionID]
		if !found {
			continue
		}
		_, stillPending := pending[versionID]
		visible := stillPending && evaluation.GoverningIntent == currentIntent
		unresolved := unresolvedRequirements(evaluation, responses[versionID])
		unresolvedByPolicy := make(map[string]Requirement, len(unresolved))
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
		return nil, false, ErrRequirementNotFound
	}
	return cloneRequirements(result), false, nil
}
