package controlhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/principal"
)

const maxResponseBytes = 2 * 1024 * 1024
const defaultRequestTimeout = 30 * time.Second

type Client struct {
	Server     string
	HTTPClient *http.Client
}

func (client Client) Intent(ctx context.Context) (IntentFact, error) {
	var fact IntentFact
	if err := client.doJSON(ctx, http.MethodGet, "/v1/intent", nil, "", &fact); err != nil {
		return IntentFact{}, fmt.Errorf("read accepted Intent: %w", err)
	}
	if fact.Schema != IntentSchema {
		return IntentFact{}, fmt.Errorf("unsupported intent schema %q", fact.Schema)
	}
	if !canonicalText(fact.Repository, 256) || !canonicalText(fact.Intent, 256) || (fact.PreviousIntent != "" && !canonicalText(fact.PreviousIntent, 256)) || !validContent(fact.Content) {
		return IntentFact{}, errors.New("Intent response is missing required identity or content")
	}
	return fact, nil
}

func (client Client) Propose(ctx context.Context, idempotencyKey string, proposal ProposalRequest) (ProposalReceipt, error) {
	if !canonicalText(idempotencyKey, 256) {
		return ProposalReceipt{}, errors.New("Idempotency-Key must be canonical text of at most 256 bytes")
	}
	if proposal.Schema != ProposalSchema {
		return ProposalReceipt{}, fmt.Errorf("unsupported proposal schema %q", proposal.Schema)
	}
	encoded, err := json.Marshal(proposal)
	if err != nil {
		return ProposalReceipt{}, fmt.Errorf("encode proposal: %w", err)
	}
	var receipt ProposalReceipt
	if err := client.doJSON(ctx, http.MethodPost, "/v1/proposals", encoded, idempotencyKey, &receipt); err != nil {
		return ProposalReceipt{}, fmt.Errorf("propose content: %w", err)
	}
	if receipt.Schema != ProposalReceiptSchema {
		return ProposalReceipt{}, fmt.Errorf("unsupported proposal receipt schema %q", receipt.Schema)
	}
	if !canonicalText(receipt.Repository, 256) || !canonicalText(receipt.Change.ID, 256) || !validVersionFact(receipt.Version) || receipt.Version.Change != receipt.Change.ID {
		return ProposalReceipt{}, errors.New("proposal receipt is missing required identity, content, or provenance")
	}
	if receipt.Version.BaseIntent != proposal.BaseIntent || receipt.Version.Content != proposal.Content || !slices.Equal(receipt.Version.Dependencies, proposal.Dependencies) {
		return ProposalReceipt{}, errors.New("proposal receipt does not match the proposed content and dependencies")
	}
	return receipt, nil
}

func (client Client) Version(ctx context.Context, versionID string) (VersionInspection, error) {
	if !canonicalText(versionID, 256) || strings.Contains(versionID, "/") {
		return VersionInspection{}, errors.New("Version id must be canonical text of at most 256 bytes")
	}
	var inspection VersionInspection
	if err := client.doJSON(ctx, http.MethodGet, "/v1/versions/"+url.PathEscape(versionID), nil, "", &inspection); err != nil {
		return VersionInspection{}, fmt.Errorf("inspect Version: %w", err)
	}
	if inspection.Schema != VersionSchema {
		return VersionInspection{}, fmt.Errorf("unsupported Version schema %q", inspection.Schema)
	}
	if inspection.Repository == "" || inspection.Version.ID != versionID || !validVersionFact(inspection.Version) {
		return VersionInspection{}, errors.New("Version inspection is missing required identity, content, or provenance")
	}
	if inspection.Evaluation != nil && (!validEvaluationFact(*inspection.Evaluation) || inspection.Evaluation.Version != versionID || inspection.Evaluation.GoverningIntent != inspection.Version.BaseIntent) {
		return VersionInspection{}, errors.New("Version Evaluation does not match its governing Intent")
	}
	if len(inspection.Requirements) > 0 && inspection.Evaluation == nil {
		return VersionInspection{}, errors.New("Version Requirements have no recorded Evaluation")
	}
	for _, requirement := range inspection.Requirements {
		if !validRequirementFact(requirement) || requirement.Version != versionID {
			return VersionInspection{}, errors.New("Version inspection contains an invalid Requirement")
		}
	}
	if inspection.Promotion != nil && (inspection.Evaluation == nil || !validPromotionFact(*inspection.Promotion) || inspection.Promotion.Version != versionID) {
		return VersionInspection{}, errors.New("Version Promotion does not match its recorded Evaluation")
	}
	return inspection, nil
}

func (client Client) Requirements(ctx context.Context, cursor string, limit int) (RequirementsPage, error) {
	if cursor != "" && !canonicalText(cursor, 4096) {
		return RequirementsPage{}, errors.New("Requirement cursor must be canonical text of at most 4096 bytes")
	}
	if limit < 1 || limit > 100 {
		return RequirementsPage{}, errors.New("Requirement page limit must be between one and 100")
	}
	query := url.Values{"limit": {fmt.Sprint(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	var page RequirementsPage
	if err := client.doJSON(ctx, http.MethodGet, "/v1/requirements?"+query.Encode(), nil, "", &page); err != nil {
		return RequirementsPage{}, fmt.Errorf("list Requirements: %w", err)
	}
	if page.Schema != RequirementsSchema {
		return RequirementsPage{}, fmt.Errorf("unsupported Requirements schema %q", page.Schema)
	}
	if !canonicalText(page.Repository, 256) || len(page.Requirements) > limit || (page.NextCursor != "" && !canonicalText(page.NextCursor, 4096)) {
		return RequirementsPage{}, errors.New("Requirement page is missing repository identity or has an invalid cursor")
	}
	for _, requirement := range page.Requirements {
		if !validRequirementFact(requirement) {
			return RequirementsPage{}, errors.New("Requirement page contains an invalid Requirement fact")
		}
	}
	return page, nil
}

func (client Client) RespondRequirement(ctx context.Context, idempotencyKey string, request RequirementResponseRequest) (RequirementResponseReceipt, error) {
	if !canonicalText(idempotencyKey, 256) {
		return RequirementResponseReceipt{}, errors.New("Idempotency-Key must be canonical text of at most 256 bytes")
	}
	if request.Schema != RequirementResponseSchema || !canonicalText(request.Version, 256) || !canonicalText(request.Policy, 256) ||
		!canonicalText(request.Rationale, 4096) || (request.Decision != "satisfied" && request.Decision != "revision_requested") {
		return RequirementResponseReceipt{}, errors.New("invalid Requirement Response")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return RequirementResponseReceipt{}, fmt.Errorf("encode Requirement Response: %w", err)
	}
	var receipt RequirementResponseReceipt
	if err := client.doJSON(ctx, http.MethodPost, "/v1/requirement-responses", encoded, idempotencyKey, &receipt); err != nil {
		return RequirementResponseReceipt{}, fmt.Errorf("record Requirement Response: %w", err)
	}
	if receipt.Schema != RequirementResponseReceiptSchema {
		return RequirementResponseReceipt{}, fmt.Errorf("unsupported Requirement Response receipt schema %q", receipt.Schema)
	}
	response := receipt.Response
	if !canonicalText(receipt.Repository, 256) || !validRequirementResponseFact(response) ||
		response.Version != request.Version || response.Policy != request.Policy || response.Decision != request.Decision || response.Rationale != request.Rationale {
		return RequirementResponseReceipt{}, errors.New("Requirement Response receipt does not match the requested immutable fact")
	}
	return receipt, nil
}

func (client Client) History(ctx context.Context, cursor string, limit int) (HistoryPage, error) {
	stream, previous, err := decodeHistoryCursor(cursor)
	if err != nil {
		return HistoryPage{}, errors.New("invalid history cursor")
	}
	if limit < 1 || limit > 100 {
		return HistoryPage{}, errors.New("history page limit must be between one and 100")
	}
	query := url.Values{"limit": {fmt.Sprint(limit)}}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	var page HistoryPage
	if err := client.doJSON(ctx, http.MethodGet, "/v1/history?"+query.Encode(), nil, "", &page); err != nil {
		return HistoryPage{}, fmt.Errorf("read history: %w", err)
	}
	if page.Schema != HistorySchema {
		return HistoryPage{}, fmt.Errorf("unsupported history schema %q", page.Schema)
	}
	if !canonicalText(page.Repository, 256) || len(page.Facts) > limit {
		return HistoryPage{}, errors.New("history page is missing repository identity")
	}
	last := previous
	for _, fact := range page.Facts {
		factStream, current, err := decodeHistoryCursor(fact.Cursor)
		if err != nil || factStream == "" || (stream != "" && factStream != stream) || current <= last || !validHistoryEntry(fact) {
			return HistoryPage{}, errors.New("history page contains an invalid or non-monotonic fact")
		}
		stream = factStream
		last = current
	}
	if page.NextCursor != "" {
		nextStream, next, err := decodeHistoryCursor(page.NextCursor)
		if err != nil || len(page.Facts) == 0 || nextStream != stream || next != last {
			return HistoryPage{}, errors.New("history page has an invalid continuation cursor")
		}
	}
	return page, nil
}

func validHistoryEntry(entry HistoryEntry) bool {
	payloads := 0
	for _, present := range []bool{
		entry.Intent != nil,
		entry.Change != nil,
		entry.Version != nil,
		entry.Evaluation != nil,
		entry.Response != nil,
		entry.Promotion != nil,
		entry.Amendment != nil,
		entry.DependentReconciliation != nil,
		entry.HeldVersionRebase != nil,
		entry.Conflict != nil,
		entry.Resolution != nil,
	} {
		if present {
			payloads++
		}
	}
	switch entry.Kind {
	case string(intent.HistoryIntentInitialized):
		return payloads == 1 && entry.Intent != nil && validHistoryIntent(*entry.Intent)
	case string(intent.HistoryVersionProposed):
		return payloads == 2 && entry.Change != nil && entry.Version != nil && canonicalText(entry.Change.ID, 256) && validVersionFact(*entry.Version) && entry.Change.ID == entry.Version.Change
	case string(intent.HistoryEvaluationRecorded):
		return payloads == 1 && entry.Evaluation != nil && validEvaluationFact(*entry.Evaluation)
	case string(intent.HistoryRequirementResponded):
		return payloads == 1 && entry.Response != nil && validRequirementResponseFact(*entry.Response)
	case string(intent.HistoryVersionPromoted):
		return payloads == 2 && entry.Intent != nil && entry.Promotion != nil && validHistoryIntent(*entry.Intent) && validPromotionFact(*entry.Promotion) && entry.Intent.ID == entry.Promotion.ToIntent && entry.Intent.PreviousID == entry.Promotion.FromIntent
	case string(intent.HistoryVersionAmended):
		return payloads == 2 && entry.Version != nil && entry.Amendment != nil && validVersionFact(*entry.Version) && validAmendmentFact(*entry.Amendment) && entry.Version.ID == entry.Amendment.ToVersion
	case string(intent.HistoryDependentReconciled):
		return payloads == 2 && entry.Version != nil && entry.DependentReconciliation != nil && validVersionFact(*entry.Version) && validDependentReconciliationFact(*entry.DependentReconciliation) && entry.Version.ID == entry.DependentReconciliation.ToVersion
	case string(intent.HistoryHeldVersionRebased):
		return payloads == 2 && entry.Version != nil && entry.HeldVersionRebase != nil && validVersionFact(*entry.Version) && validHeldVersionRebaseFact(*entry.HeldVersionRebase) && entry.Version.ID == entry.HeldVersionRebase.ToVersion && entry.Version.BaseIntent == entry.HeldVersionRebase.ToIntent
	case string(intent.HistoryConflictRecorded):
		return payloads == 1 && entry.Conflict != nil && validConflictFact(*entry.Conflict)
	case string(intent.HistoryReconciliationResolved):
		return payloads == 2 && entry.Version != nil && entry.Resolution != nil && validVersionFact(*entry.Version) && validResolutionFact(*entry.Resolution) && entry.Version.ID == entry.Resolution.ToVersion && entry.Version.BaseIntent == entry.Resolution.BaseIntent
	default:
		return false
	}
}

func validHistoryIntent(fact HistoryIntentFact) bool {
	return canonicalText(fact.ID, 256) && (fact.PreviousID == "" || canonicalText(fact.PreviousID, 256)) && validContent(fact.Content)
}

func validEvaluationFact(fact EvaluationFact) bool {
	if !canonicalText(fact.Version, 256) || !canonicalText(fact.GoverningIntent, 256) || len(fact.Policies) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(fact.Policies))
	for _, policy := range fact.Policies {
		assignee, validAssignee := principal.Canonical(policy.Assignee)
		if !canonicalText(policy.Policy, 256) || !canonicalText(policy.Instruction, 256*1024) || !validAssignee || assignee != policy.Assignee ||
			!boundedText(policy.Reason, maxResponseBytes) || len(policy.Evidence) == 0 {
			return false
		}
		if _, duplicate := seen[policy.Policy]; duplicate {
			return false
		}
		seen[policy.Policy] = struct{}{}
		for _, evidence := range policy.Evidence {
			if !boundedText(evidence, maxResponseBytes) {
				return false
			}
		}
		provenance := policy.Provenance
		if (provenance.Evaluator == "") != (provenance.ContractRevision == "") ||
			(provenance.Evaluator != "" && (!canonicalText(provenance.Evaluator, 256) || !canonicalText(provenance.ContractRevision, 128))) {
			return false
		}
	}
	return true
}

func validRequirementResponseFact(fact RequirementResponseFact) bool {
	assignee, validAssignee := principal.Canonical(fact.Assignee)
	return canonicalText(fact.ID, 256) && canonicalText(fact.Version, 256) && canonicalText(fact.Policy, 256) && validAssignee && assignee == fact.Assignee &&
		(fact.Decision == "satisfied" || fact.Decision == "revision_requested") && boundedText(fact.Rationale, 4096)
}

func validPromotionFact(fact PromotionFact) bool {
	return canonicalText(fact.ID, 256) && canonicalText(fact.FromIntent, 256) && canonicalText(fact.ToIntent, 256) && canonicalText(fact.Version, 256)
}

func validAmendmentFact(fact AmendmentFact) bool {
	return canonicalText(fact.FromVersion, 256) && canonicalText(fact.ToVersion, 256) && boundedText(fact.Rationale, 4096)
}

func validDependentReconciliationFact(fact DependentReconciliationFact) bool {
	return canonicalText(fact.FromVersion, 256) && canonicalText(fact.ToVersion, 256) && canonicalText(fact.ReplacedDependency, 256) &&
		canonicalText(fact.AcceptedVersion, 256) && canonicalText(fact.BaseIntent, 256) && boundedText(fact.Rationale, 4096)
}

func validHeldVersionRebaseFact(fact HeldVersionRebaseFact) bool {
	return canonicalText(fact.FromVersion, 256) && canonicalText(fact.ToVersion, 256) && canonicalText(fact.FromIntent, 256) && canonicalText(fact.ToIntent, 256) && boundedText(fact.Rationale, 4096)
}

func validConflictFact(fact ReconciliationConflictFact) bool {
	reporter, validReporter := principal.Canonical(fact.ReportedBy)
	paths, err := intent.NormalizeReconciliationConflictPaths(fact.AffectedPaths)
	return canonicalText(fact.ID, 256) && canonicalText(fact.Change.ID, 256) && validVersionFact(fact.Version) && fact.Change.ID == fact.Version.Change &&
		canonicalText(fact.FromVersion, 256) && canonicalText(fact.ToVersion, 256) && canonicalText(fact.BaseIntent, 256) && validReporter && reporter == fact.ReportedBy &&
		err == nil && slices.Equal(paths, fact.AffectedPaths)
}

func validResolutionFact(fact ReconciliationResolutionFact) bool {
	resolver, validResolver := principal.Canonical(fact.ResolvedBy)
	return canonicalText(fact.ID, 256) && canonicalText(fact.ConflictID, 256) && canonicalText(fact.FromVersion, 256) && canonicalText(fact.ToVersion, 256) &&
		canonicalText(fact.BaseIntent, 256) && validResolver && resolver == fact.ResolvedBy && boundedText(fact.Rationale, 4096)
}

func boundedText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && value == strings.TrimSpace(value) && !strings.ContainsRune(value, '\x00')
}

func validRequirementFact(requirement RequirementFact) bool {
	assignee, validAssignee := principal.Canonical(requirement.Assignee)
	if !canonicalText(requirement.Version, 256) || !canonicalText(requirement.Policy, 256) || !validAssignee || assignee != requirement.Assignee ||
		!boundedText(requirement.Reason, maxResponseBytes) {
		return false
	}
	if len(requirement.Evidence) == 0 {
		return false
	}
	for _, evidence := range requirement.Evidence {
		if !boundedText(evidence, maxResponseBytes) {
			return false
		}
	}
	if requirement.LatestResponse == nil {
		return true
	}
	response := requirement.LatestResponse
	return validRequirementResponseFact(*response) && response.Version == requirement.Version && response.Policy == requirement.Policy && response.Assignee == requirement.Assignee
}

func (client Client) doJSON(ctx context.Context, method string, path string, body []byte, idempotencyKey string, target any) error {
	endpoint, err := controlEndpoint(client.Server, path)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create control request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultRequestTimeout}
	}
	redirectSafeClient := *httpClient
	redirectSafeClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("control client refuses redirects")
	}
	response, err := redirectSafeClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read control response: %w", err)
	}
	if len(encoded) > maxResponseBytes {
		return errors.New("control response exceeds 2 MiB")
	}
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK {
		if mediaErr == nil && mediaType == "application/json" {
			var problem errorFact
			if json.Unmarshal(encoded, &problem) == nil && problem.Schema == errorSchema && canonicalText(problem.Message, 4096) {
				return errors.New(problem.Message)
			}
		}
		return fmt.Errorf("server returned %s", response.Status)
	}
	if mediaErr != nil || mediaType != "application/json" {
		return errors.New("control response is not application/json")
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("decode control response: response must contain a single JSON value: %w", err)
	}
	return nil
}

func controlEndpoint(server string, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(server))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("server must be an absolute HTTP URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("server URL must not contain credentials, a query, or a fragment")
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || !address.IsLoopback() {
		return "", errors.New("server must use a numeric loopback address")
	}
	relative, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(relative.Path, "/") || relative.IsAbs() || relative.Host != "" || relative.User != nil || relative.Fragment != "" {
		return "", errors.New("control path must be an absolute-path reference")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + relative.Path
	parsed.RawQuery = relative.RawQuery
	return parsed.String(), nil
}
