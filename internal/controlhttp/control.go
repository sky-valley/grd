// Package controlhttp projects GRD repository facts over HTTP.
package controlhttp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/intentservice"
	"github.com/sky-valley/grd/internal/principal"
)

const maxRequestBytes = 64 * 1024

func NewHandler(config Config, service Service) (http.Handler, error) {
	if config.Repository == "" || config.Repository != strings.TrimSpace(config.Repository) || strings.ContainsAny(config.Repository, "\x00\r\n") {
		return nil, errors.New("control repository id must be canonical one-line text")
	}
	producer, valid := principal.Canonical(config.Producer)
	if !valid || producer != config.Producer {
		return nil, errors.New("control producer must be a canonical principal subject")
	}
	if service == nil {
		return nil, errors.New("control service is required")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/v1/intent":
			serveIntent(w, r, config.Repository, service)
		case "/v1/proposals":
			serveProposal(w, r, config, service)
		default:
			if versionID, found := strings.CutPrefix(r.URL.Path, "/v1/versions/"); found && versionID != "" && !strings.Contains(versionID, "/") {
				serveVersion(w, r, config.Repository, intent.VersionID(versionID), service)
				return
			}
			writeError(w, http.StatusNotFound, "control endpoint not found")
		}
	})
	return handler, nil
}

func serveVersion(w http.ResponseWriter, r *http.Request, repository string, versionID intent.VersionID, service Service) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	version, found, err := service.Version(r.Context(), repository, versionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Version could not be read")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "Version not found")
		return
	}
	inspection := VersionInspection{
		Schema:     VersionSchema,
		Repository: repository,
		Version:    mapVersion(version),
	}
	promoted, promotionFound, err := service.Promotion(r.Context(), repository, versionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Version Promotion could not be read")
		return
	}
	evaluation, evaluated, err := service.Evaluation(r.Context(), repository, versionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Version Evaluation could not be read")
		return
	}
	if evaluated {
		mapped := mapEvaluation(evaluation)
		inspection.Evaluation = &mapped
		requirements, err := service.Requirements(r.Context(), repository, versionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Version Requirements could not be read")
			return
		}
		inspection.Requirements = mapRequirements(requirements)
	}
	if promotionFound && !evaluated {
		writeError(w, http.StatusInternalServerError, "Version Promotion has no recorded Evaluation")
		return
	}
	if promotionFound {
		mapped := PromotionFact{
			ID:         string(promoted.Promotion.ID),
			FromIntent: string(promoted.Promotion.FromIntent),
			ToIntent:   string(promoted.Promotion.ToIntent),
			Version:    string(promoted.Promotion.VersionID),
		}
		inspection.Promotion = &mapped
	}
	writeJSON(w, http.StatusOK, inspection)
}

func mapVersion(version intent.Version) VersionFact {
	dependencies := make([]string, len(version.Dependencies))
	for index, dependency := range version.Dependencies {
		dependencies[index] = string(dependency)
	}
	return VersionFact{
		ID:         string(version.ID),
		Change:     string(version.ChangeID),
		BaseIntent: string(version.BaseIntent),
		Content: Content{
			Engine:   version.Content.Engine,
			Revision: version.Content.Revision,
		},
		Producer:     version.Producer,
		Dependencies: dependencies,
	}
}

func mapEvaluation(evaluation intent.Evaluation) EvaluationFact {
	policies := make([]PolicyEvaluationFact, len(evaluation.PolicyEvaluations))
	for index, policy := range evaluation.PolicyEvaluations {
		policies[index] = PolicyEvaluationFact{
			Policy:         policy.Policy,
			Instruction:    policy.Instruction,
			Assignee:       policy.Assignee,
			RequiresAction: policy.RequiresAction,
			Reason:         policy.Reason,
			Evidence:       append([]string(nil), policy.Evidence...),
			Provenance: ProvenanceFact{
				Evaluator:        policy.Provenance.Evaluator,
				ContractRevision: policy.Provenance.ContractRevision,
			},
		}
	}
	return EvaluationFact{GoverningIntent: string(evaluation.GoverningIntent), Policies: policies}
}

func mapRequirements(requirements []intent.Requirement) []RequirementFact {
	mapped := make([]RequirementFact, len(requirements))
	for index, requirement := range requirements {
		mapped[index] = RequirementFact{
			Policy:   requirement.Policy,
			Assignee: requirement.Assignee,
			Reason:   requirement.Reason,
			Evidence: append([]string(nil), requirement.Evidence...),
		}
		if requirement.LatestResponse != nil {
			mapped[index].LatestResponse = &RequirementResponseFact{
				ID:        string(requirement.LatestResponse.ID),
				Assignee:  requirement.LatestResponse.Assignee,
				Decision:  string(requirement.LatestResponse.Decision),
				Rationale: requirement.LatestResponse.Rationale,
			}
		}
	}
	return mapped
}

func serveIntent(w http.ResponseWriter, r *http.Request, repositoryID string, service Service) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	accepted, err := service.CurrentIntent(r.Context(), repositoryID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "accepted Intent could not be read")
		return
	}
	writeJSON(w, http.StatusOK, IntentFact{
		Schema:         IntentSchema,
		Repository:     repositoryID,
		Intent:         string(accepted.ID),
		PreviousIntent: string(accepted.PreviousID),
		Content: Content{
			Engine:   accepted.Content.Engine,
			Revision: accepted.Content.Revision,
		},
	})
}

func serveProposal(w http.ResponseWriter, r *http.Request, config Config, service Service) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "proposal requires application/json")
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if !canonicalText(idempotencyKey, 256) {
		writeError(w, http.StatusBadRequest, "Idempotency-Key must be canonical text of at most 256 bytes")
		return
	}
	var request ProposalRequest
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "request body must be one valid proposal JSON value")
		return
	}
	if request.Schema != ProposalSchema {
		writeError(w, http.StatusBadRequest, "unsupported proposal schema")
		return
	}
	if !canonicalText(request.BaseIntent, 256) {
		writeError(w, http.StatusBadRequest, "baseIntent must be canonical text of at most 256 bytes")
		return
	}
	if !canonicalText(request.Content.Engine, 64) || !canonicalText(request.Content.Revision, 1024) {
		writeError(w, http.StatusBadRequest, "content requires canonical engine and revision")
		return
	}
	dependencies := make([]intent.VersionID, len(request.Dependencies))
	seenDependencies := make(map[string]struct{}, len(request.Dependencies))
	for index, dependency := range request.Dependencies {
		if !canonicalText(dependency, 256) {
			writeError(w, http.StatusBadRequest, "dependencies must contain canonical Version ids")
			return
		}
		if _, duplicate := seenDependencies[dependency]; duplicate {
			writeError(w, http.StatusBadRequest, "dependencies must contain unique Version ids")
			return
		}
		seenDependencies[dependency] = struct{}{}
		dependencies[index] = intent.VersionID(dependency)
	}
	proposed, err := service.Propose(r.Context(), config.Repository, intentservice.Proposal{
		IdempotencyKey: idempotencyKey,
		BaseIntent:     intent.RevisionID(request.BaseIntent),
		Content: intent.ContentRef{
			Engine:   request.Content.Engine,
			Revision: request.Content.Revision,
		},
		Producer:     config.Producer,
		Dependencies: dependencies,
	})
	if err != nil {
		writeProposalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mapProposalReceipt(config.Repository, proposed))
}

func mapProposalReceipt(repository string, proposed intent.Proposed) ProposalReceipt {
	return ProposalReceipt{
		Schema:     ProposalReceiptSchema,
		Repository: repository,
		Change:     ChangeFact{ID: string(proposed.Change.ID)},
		Version:    mapVersion(proposed.Version),
	}
}

func writeProposalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, intentservice.ErrRepositoryNotFound):
		writeError(w, http.StatusNotFound, "repository not found")
	case errors.Is(err, intent.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "Idempotency-Key was already used for a different proposal")
	case errors.Is(err, intent.ErrIntentNotFound):
		writeError(w, http.StatusUnprocessableEntity, "baseIntent does not identify accepted repository Intent")
	case errors.Is(err, intent.ErrVersionNotFound):
		writeError(w, http.StatusUnprocessableEntity, "dependency does not identify an admitted Version")
	case errors.Is(err, intent.ErrContentNotAdmissible):
		writeError(w, http.StatusUnprocessableEntity, "content cannot be admitted by this repository engine")
	default:
		writeError(w, http.StatusInternalServerError, "proposal could not be admitted")
	}
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorFact{Schema: errorSchema, Message: message})
}
