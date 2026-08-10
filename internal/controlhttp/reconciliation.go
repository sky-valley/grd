package controlhttp

import (
	"errors"
	"mime"
	"net/http"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/intentservice"
)

func serveChange(w http.ResponseWriter, r *http.Request, repository string, changeID intent.ChangeID, service Service) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !canonicalText(string(changeID), 256) {
		writeError(w, http.StatusBadRequest, "invalid Change id")
		return
	}
	inspection, err := service.InspectChange(r.Context(), repository, changeID)
	if err != nil {
		if errors.Is(err, intent.ErrChangeNotFound) {
			writeError(w, http.StatusNotFound, "Change not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "Change could not be read")
		return
	}
	fact := ChangeInspection{Schema: ChangeSchema, Repository: repository, Change: ChangeFact{ID: string(inspection.Change.ID)}, LatestVersion: mapVersion(inspection.LatestVersion)}
	if inspection.LatestAmendment != nil {
		fact.LatestAmendment = &AmendmentFact{FromVersion: string(inspection.LatestAmendment.FromVersion), ToVersion: string(inspection.LatestAmendment.ToVersion), Rationale: inspection.LatestAmendment.Rationale}
	}
	if inspection.LatestPromotion != nil {
		promotion := inspection.LatestPromotion.Promotion
		fact.LatestPromotion = &PromotionFact{ID: string(promotion.ID), FromIntent: string(promotion.FromIntent), ToIntent: string(promotion.ToIntent), Version: string(promotion.VersionID)}
	}
	writeJSON(w, http.StatusOK, fact)
}

func serveAmendment(w http.ResponseWriter, r *http.Request, config Config, service Service) {
	var request AmendmentRequest
	if !decodeMutation(w, r, &request) {
		return
	}
	if request.Schema != AmendmentSchema || !canonicalText(request.Change, 256) || !canonicalText(request.ExpectedVersion, 256) || !validContent(request.Content) || !canonicalText(request.Rationale, 4096) {
		writeError(w, http.StatusBadRequest, "invalid amendment")
		return
	}
	amended, err := service.Amend(r.Context(), config.Repository, intentservice.AmendmentRequest{IdempotencyKey: r.Header.Get("Idempotency-Key"), ChangeID: intent.ChangeID(request.Change), ExpectedVersion: intent.VersionID(request.ExpectedVersion), Content: mapContentRef(request.Content), Producer: config.Producer, Rationale: request.Rationale})
	if err != nil {
		writeReconciliationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, AmendmentReceipt{Schema: AmendmentReceiptSchema, Repository: config.Repository, Change: ChangeFact{ID: string(amended.Change.ID)}, Version: mapVersion(amended.Version), Amendment: AmendmentFact{FromVersion: string(amended.Amendment.FromVersion), ToVersion: string(amended.Amendment.ToVersion), Rationale: amended.Amendment.Rationale}})
}

func serveHeldVersionRebase(w http.ResponseWriter, r *http.Request, config Config, service Service) {
	var request HeldVersionRebaseRequest
	if !decodeMutation(w, r, &request) {
		return
	}
	if request.Schema != HeldVersionRebaseSchema || !canonicalText(request.ExpectedVersion, 256) || !canonicalText(request.ExpectedIntent, 256) || !validContent(request.Content) || !canonicalText(request.Rationale, 4096) {
		writeError(w, http.StatusBadRequest, "invalid held Version rebase")
		return
	}
	rebased, err := service.RebaseHeldVersion(r.Context(), config.Repository, intentservice.HeldVersionRebaseRequest{IdempotencyKey: r.Header.Get("Idempotency-Key"), ExpectedVersion: intent.VersionID(request.ExpectedVersion), ExpectedIntent: intent.RevisionID(request.ExpectedIntent), Content: mapContentRef(request.Content), Producer: config.Producer, Rationale: request.Rationale})
	if err != nil {
		writeReconciliationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, HeldVersionRebaseReceipt{Schema: HeldVersionRebaseReceiptSchema, Repository: config.Repository, Change: ChangeFact{ID: string(rebased.Change.ID)}, Version: mapVersion(rebased.Version), Rebase: HeldVersionRebaseFact{FromVersion: string(rebased.Rebase.FromVersion), ToVersion: string(rebased.Rebase.ToVersion), FromIntent: string(rebased.Rebase.FromIntent), ToIntent: string(rebased.Rebase.ToIntent), Rationale: rebased.Rebase.Rationale}})
}

func serveDependentReconciliation(w http.ResponseWriter, r *http.Request, config Config, service Service) {
	var request DependentReconciliationRequest
	if !decodeMutation(w, r, &request) {
		return
	}
	if request.Schema != DependentReconciliationSchema || !canonicalText(request.ExpectedVersion, 256) || !canonicalText(request.ReplacedDependency, 256) || !canonicalText(request.AcceptedVersion, 256) || !canonicalText(request.ExpectedIntent, 256) || !validContent(request.Content) || !canonicalText(request.Rationale, 4096) {
		writeError(w, http.StatusBadRequest, "invalid dependent reconciliation")
		return
	}
	reconciled, err := service.ReconcileDependent(r.Context(), config.Repository, intentservice.DependentReconciliationRequest{IdempotencyKey: r.Header.Get("Idempotency-Key"), ExpectedVersion: intent.VersionID(request.ExpectedVersion), ReplacedDependency: intent.VersionID(request.ReplacedDependency), AcceptedVersion: intent.VersionID(request.AcceptedVersion), ExpectedIntent: intent.RevisionID(request.ExpectedIntent), Content: mapContentRef(request.Content), Producer: config.Producer, Rationale: request.Rationale})
	if err != nil {
		writeReconciliationError(w, err)
		return
	}
	reconciliation := reconciled.Reconciliation
	writeJSON(w, http.StatusOK, DependentReconciliationReceipt{Schema: DependentReconciliationReceiptSchema, Repository: config.Repository, Change: ChangeFact{ID: string(reconciled.Change.ID)}, Version: mapVersion(reconciled.Version), Reconciliation: DependentReconciliationFact{FromVersion: string(reconciliation.FromVersion), ToVersion: string(reconciliation.ToVersion), ReplacedDependency: string(reconciliation.ReplacedDependency), AcceptedVersion: string(reconciliation.AcceptedVersion), BaseIntent: string(reconciliation.BaseIntent), Rationale: reconciliation.Rationale}})
}

func serveReconciliationConflict(w http.ResponseWriter, r *http.Request, config Config, service Service) {
	var request ReconciliationConflictRequest
	if !decodeMutation(w, r, &request) {
		return
	}
	if request.Schema != ReconciliationConflictSchema || !canonicalText(request.FromVersion, 256) || !canonicalText(request.ToVersion, 256) || !canonicalText(request.DescendantVersion, 256) || (request.ExpectedIntent != "" && !canonicalText(request.ExpectedIntent, 256)) {
		writeError(w, http.StatusBadRequest, "invalid reconciliation conflict")
		return
	}
	inspection, err := service.RecordReconciliationConflict(r.Context(), config.Repository, intentservice.ReconciliationConflictRequest{IdempotencyKey: r.Header.Get("Idempotency-Key"), FromVersion: intent.VersionID(request.FromVersion), ToVersion: intent.VersionID(request.ToVersion), DescendantVersion: intent.VersionID(request.DescendantVersion), ExpectedIntent: intent.RevisionID(request.ExpectedIntent), ReportedBy: config.Producer, AffectedPaths: request.AffectedPaths})
	if err != nil {
		writeReconciliationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ReconciliationConflictReceipt{Schema: ReconciliationConflictReceiptSchema, Repository: config.Repository, Conflict: mapConflict(inspection.ReconciliationConflict)})
}

func serveReconciliationResolution(w http.ResponseWriter, r *http.Request, config Config, service Service) {
	var request ReconciliationResolutionRequest
	if !decodeMutation(w, r, &request) {
		return
	}
	if request.Schema != ReconciliationResolutionSchema || !canonicalText(request.Conflict, 256) || !canonicalText(request.ExpectedVersion, 256) || !canonicalText(request.ExpectedIntent, 256) || !validContent(request.Content) || !canonicalText(request.Rationale, 4096) {
		writeError(w, http.StatusBadRequest, "invalid reconciliation resolution")
		return
	}
	resolved, err := service.ResolveReconciliationConflict(r.Context(), config.Repository, intentservice.ReconciliationResolutionRequest{IdempotencyKey: r.Header.Get("Idempotency-Key"), ConflictID: intent.ConflictID(request.Conflict), ExpectedVersion: intent.VersionID(request.ExpectedVersion), ExpectedIntent: intent.RevisionID(request.ExpectedIntent), Content: mapContentRef(request.Content), Producer: config.Producer, ResolvedBy: config.Producer, Rationale: request.Rationale})
	if err != nil {
		writeReconciliationError(w, err)
		return
	}
	resolution := resolved.Resolution
	writeJSON(w, http.StatusOK, ReconciliationResolutionReceipt{Schema: ReconciliationResolutionReceiptSchema, Repository: config.Repository, Change: ChangeFact{ID: string(resolved.Change.ID)}, Version: mapVersion(resolved.Version), Resolution: ReconciliationResolutionFact{ID: string(resolution.ID), ConflictID: string(resolution.ConflictID), FromVersion: string(resolution.FromVersion), ToVersion: string(resolution.ToVersion), BaseIntent: string(resolution.BaseIntent), ResolvedBy: resolution.ResolvedBy, Rationale: resolution.Rationale}})
}

func decodeMutation(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "mutation does not accept query parameters")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "mutation requires application/json")
		return false
	}
	if !canonicalText(r.Header.Get("Idempotency-Key"), 256) {
		writeError(w, http.StatusBadRequest, "Idempotency-Key must be canonical text of at most 256 bytes")
		return false
	}
	if err := decodeRequest(w, r, target); err != nil {
		writeError(w, http.StatusBadRequest, "request body must contain one valid mutation JSON value")
		return false
	}
	return true
}

func validContent(content Content) bool {
	return canonicalText(content.Engine, 64) && canonicalText(content.Revision, 1024)
}

func mapContentRef(content Content) intent.ContentRef {
	return intent.ContentRef{Engine: content.Engine, Revision: content.Revision}
}

func mapConflict(conflict intent.ReconciliationConflict) ReconciliationConflictFact {
	return ReconciliationConflictFact{ID: string(conflict.ID), Change: ChangeFact{ID: string(conflict.Change.ID)}, Version: mapVersion(conflict.Version), FromVersion: string(conflict.FromVersion), ToVersion: string(conflict.ToVersion), BaseIntent: string(conflict.BaseIntent), ReportedBy: conflict.ReportedBy, AffectedPaths: append([]string(nil), conflict.AffectedPaths...)}
}

func writeReconciliationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, intent.ErrIdempotencyConflict), errors.Is(err, intent.ErrIntentAdvanced), errors.Is(err, intent.ErrVersionAdvanced), errors.Is(err, intent.ErrVersionPromotionStarted), errors.Is(err, intent.ErrPromotionPending), errors.Is(err, intent.ErrReconciliationConflictResolved):
		writeError(w, http.StatusConflict, "repository facts advanced; refresh and retry with current immutable facts")
	case errors.Is(err, intent.ErrChangeNotFound), errors.Is(err, intent.ErrVersionNotFound), errors.Is(err, intent.ErrReconciliationConflictNotFound):
		writeError(w, http.StatusNotFound, "referenced repository fact was not found")
	case errors.Is(err, intent.ErrContentNotAdmissible):
		writeError(w, http.StatusUnprocessableEntity, "content cannot be admitted by this repository engine")
	default:
		writeError(w, http.StatusUnprocessableEntity, "reconciliation operation was not admissible")
	}
}
