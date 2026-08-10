package controlhttp

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strconv"

	"github.com/sky-valley/grd/internal/intent"
)

func serveHistory(w http.ResponseWriter, r *http.Request, repository string, service Service) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !onlyQueryKeys(r.URL.Query(), "cursor", "limit") {
		writeError(w, http.StatusBadRequest, "history accepts only cursor and limit query parameters")
		return
	}
	streamPage, err := service.History(r.Context(), repository, intent.HistoryQuery{Limit: 1})
	if err != nil || len(streamPage.Facts) == 0 || streamPage.Facts[0].Kind != intent.HistoryIntentInitialized || streamPage.Facts[0].Intent == nil {
		writeError(w, http.StatusInternalServerError, "history stream identity could not be read")
		return
	}
	stream := string(streamPage.Facts[0].Intent.ID)
	cursorStream, cursor, err := decodeHistoryCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid history cursor")
		return
	}
	if cursorStream != "" && cursorStream != stream {
		writeError(w, http.StatusBadRequest, "history cursor does not identify this repository stream")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between one and 100")
			return
		}
	}
	page, err := service.History(r.Context(), repository, intent.HistoryQuery{After: cursor, Limit: limit})
	if err != nil {
		if errors.Is(err, intent.ErrHistoryCursorNotFound) {
			writeError(w, http.StatusBadRequest, "history cursor does not identify this repository stream")
			return
		}
		writeError(w, http.StatusInternalServerError, "history could not be read")
		return
	}
	entries := make([]HistoryEntry, len(page.Facts))
	for index, fact := range page.Facts {
		entries[index] = mapHistoryFact(stream, fact)
	}
	writeJSON(w, http.StatusOK, HistoryPage{
		Schema:     HistorySchema,
		Repository: repository,
		Facts:      entries,
		NextCursor: encodeHistoryCursor(stream, page.NextCursor),
	})
}

func mapHistoryFact(stream string, fact intent.HistoryFact) HistoryEntry {
	entry := HistoryEntry{Cursor: encodeHistoryCursor(stream, fact.Cursor), Kind: string(fact.Kind)}
	if fact.Intent != nil {
		entry.Intent = &HistoryIntentFact{ID: string(fact.Intent.ID), PreviousID: string(fact.Intent.PreviousID), Content: mapContent(fact.Intent.Content)}
	}
	if fact.Change != nil {
		entry.Change = &ChangeFact{ID: string(fact.Change.ID)}
	}
	if fact.Version != nil {
		mapped := mapVersion(*fact.Version)
		entry.Version = &mapped
	}
	if fact.Evaluation != nil {
		mapped := mapEvaluation(*fact.Evaluation)
		entry.Evaluation = &mapped
	}
	if fact.RequirementResponse != nil {
		mapped := mapRequirementResponse(*fact.RequirementResponse)
		entry.Response = &mapped
	}
	if fact.Promotion != nil {
		entry.Promotion = &PromotionFact{ID: string(fact.Promotion.ID), FromIntent: string(fact.Promotion.FromIntent), ToIntent: string(fact.Promotion.ToIntent), Version: string(fact.Promotion.VersionID)}
	}
	if fact.Amendment != nil {
		entry.Amendment = &AmendmentFact{FromVersion: string(fact.Amendment.FromVersion), ToVersion: string(fact.Amendment.ToVersion), Rationale: fact.Amendment.Rationale}
	}
	if fact.DependentReconciliation != nil {
		reconciliation := fact.DependentReconciliation
		entry.DependentReconciliation = &DependentReconciliationFact{FromVersion: string(reconciliation.FromVersion), ToVersion: string(reconciliation.ToVersion), ReplacedDependency: string(reconciliation.ReplacedDependency), AcceptedVersion: string(reconciliation.AcceptedVersion), BaseIntent: string(reconciliation.BaseIntent), Rationale: reconciliation.Rationale}
	}
	if fact.HeldVersionRebase != nil {
		rebase := fact.HeldVersionRebase
		entry.HeldVersionRebase = &HeldVersionRebaseFact{FromVersion: string(rebase.FromVersion), ToVersion: string(rebase.ToVersion), FromIntent: string(rebase.FromIntent), ToIntent: string(rebase.ToIntent), Rationale: rebase.Rationale}
	}
	if fact.ReconciliationConflict != nil {
		conflict := fact.ReconciliationConflict
		entry.Conflict = &ReconciliationConflictFact{ID: string(conflict.ID), Change: ChangeFact{ID: string(conflict.Change.ID)}, Version: mapVersion(conflict.Version), FromVersion: string(conflict.FromVersion), ToVersion: string(conflict.ToVersion), BaseIntent: string(conflict.BaseIntent), ReportedBy: conflict.ReportedBy, AffectedPaths: append([]string(nil), conflict.AffectedPaths...)}
	}
	if fact.ReconciliationResolution != nil {
		resolution := fact.ReconciliationResolution
		entry.Resolution = &ReconciliationResolutionFact{ID: string(resolution.ID), ConflictID: string(resolution.ConflictID), FromVersion: string(resolution.FromVersion), ToVersion: string(resolution.ToVersion), BaseIntent: string(resolution.BaseIntent), ResolvedBy: resolution.ResolvedBy, Rationale: resolution.Rationale}
	}
	return entry
}

func mapContent(content intent.ContentRef) Content {
	return Content{Engine: content.Engine, Revision: content.Revision}
}

func encodeHistoryCursor(stream string, cursor intent.HistoryCursor) string {
	if cursor == 0 {
		return ""
	}
	var position [8]byte
	binary.BigEndian.PutUint64(position[:], uint64(cursor))
	encoded := append([]byte(stream+"\n"), position[:]...)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeHistoryCursor(encoded string) (string, intent.HistoryCursor, error) {
	if encoded == "" {
		return "", 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) < 10 {
		return "", 0, errors.New("invalid history cursor")
	}
	separator := len(decoded) - 9
	if decoded[separator] != '\n' {
		return "", 0, errors.New("invalid history cursor")
	}
	stream := string(decoded[:separator])
	if !canonicalText(stream, 256) {
		return "", 0, errors.New("invalid history cursor")
	}
	return stream, intent.HistoryCursor(binary.BigEndian.Uint64(decoded[separator+1:])), nil
}
