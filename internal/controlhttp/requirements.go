package controlhttp

import (
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sky-valley/grd/internal/intent"
)

const defaultRequirementPageSize = 50

func serveRequirements(w http.ResponseWriter, r *http.Request, config Config, service Service) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !onlyQueryKeys(r.URL.Query(), "cursor", "limit") {
		writeError(w, http.StatusBadRequest, "requirements accepts only cursor and limit query parameters")
		return
	}
	cursor, err := decodeRequirementCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid Requirement cursor")
		return
	}
	limit := defaultRequirementPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between one and 100")
			return
		}
	}
	page, err := service.PendingRequirements(r.Context(), config.Repository, intent.PendingRequirementQuery{
		Assignee: config.Producer,
		After:    cursor,
		Limit:    limit,
	})
	if err != nil {
		if errors.Is(err, intent.ErrRequirementNotFound) {
			writeError(w, http.StatusBadRequest, "Requirement cursor does not identify this pending stream")
			return
		}
		writeError(w, http.StatusInternalServerError, "pending Requirements could not be read")
		return
	}
	writeJSON(w, http.StatusOK, RequirementsPage{
		Schema:       RequirementsSchema,
		Repository:   config.Repository,
		Requirements: mapRequirements(page.Requirements),
		NextCursor:   encodeRequirementCursor(page.NextCursor),
	})
}

func serveRequirementResponse(w http.ResponseWriter, r *http.Request, config Config, service Service) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "requirement response does not accept query parameters")
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Requirement Response requires application/json")
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if !canonicalText(idempotencyKey, 256) {
		writeError(w, http.StatusBadRequest, "Idempotency-Key must be canonical text of at most 256 bytes")
		return
	}
	var request RequirementResponseRequest
	if err := decodeRequest(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "request body must be one valid Requirement Response JSON value")
		return
	}
	decision := intent.RequirementDecision(request.Decision)
	if request.Schema != RequirementResponseSchema || !canonicalText(request.Version, 256) || !canonicalText(request.Policy, 256) ||
		!canonicalText(request.Rationale, 4096) || (decision != intent.RequirementSatisfied && decision != intent.RequirementRevisionRequested) {
		writeError(w, http.StatusBadRequest, "invalid Requirement Response")
		return
	}
	response, err := service.RecordRequirementResponse(r.Context(), config.Repository, intent.RequirementResponseRequest{
		IdempotencyKey: idempotencyKey,
		VersionID:      intent.VersionID(request.Version),
		Policy:         request.Policy,
		Assignee:       config.Producer,
		Decision:       decision,
		Rationale:      request.Rationale,
	})
	if err != nil {
		writeRequirementResponseError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, RequirementResponseReceipt{
		Schema:     RequirementResponseReceiptSchema,
		Repository: config.Repository,
		Response:   mapRequirementResponse(response),
	})
}

func mapRequirementResponse(response intent.RequirementResponse) RequirementResponseFact {
	return RequirementResponseFact{
		ID:        string(response.ID),
		Version:   string(response.VersionID),
		Policy:    response.Policy,
		Assignee:  response.Assignee,
		Decision:  string(response.Decision),
		Rationale: response.Rationale,
	}
}

func writeRequirementResponseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, intent.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "Idempotency-Key was already used for a different operation")
	case errors.Is(err, intent.ErrRequirementNotAssigned):
		writeError(w, http.StatusForbidden, "Requirement is not assigned to the configured principal")
	case errors.Is(err, intent.ErrRequirementNotFound), errors.Is(err, intent.ErrVersionNotFound):
		writeError(w, http.StatusNotFound, "Requirement not found")
	case errors.Is(err, intent.ErrIntentAdvanced), errors.Is(err, intent.ErrVersionAdvanced), errors.Is(err, intent.ErrVersionPromotionStarted):
		writeError(w, http.StatusConflict, "Requirement can no longer be answered for this Version")
	default:
		writeError(w, http.StatusInternalServerError, "Requirement Response could not be recorded")
	}
}

func encodeRequirementCursor(cursor intent.RequirementCursor) string {
	if cursor.VersionID == "" && cursor.Policy == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(string(cursor.VersionID) + "\n" + cursor.Policy))
}

func decodeRequirementCursor(encoded string) (intent.RequirementCursor, error) {
	if encoded == "" {
		return intent.RequirementCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return intent.RequirementCursor{}, err
	}
	version, policy, found := strings.Cut(string(decoded), "\n")
	if !found || !canonicalText(version, 256) || !canonicalText(policy, 256) {
		return intent.RequirementCursor{}, errors.New("invalid Requirement cursor")
	}
	return intent.RequirementCursor{VersionID: intent.VersionID(version), Policy: policy}, nil
}

func onlyQueryKeys(values url.Values, allowed ...string) bool {
	known := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		known[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := known[key]; !ok || len(entries) != 1 {
			return false
		}
	}
	return true
}
