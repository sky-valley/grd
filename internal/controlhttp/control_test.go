package controlhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sky-valley/grd/internal/controlhttp"
	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/intentservice"
)

func TestIntentEndpointProjectsTheAcceptedRepositoryFact(t *testing.T) {
	accepted := intent.Revision{
		ID:         "intent_current",
		PreviousID: "intent_previous",
		Content:    intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
	}
	service := &staticService{accepted: accepted}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodGet, "/v1/intent", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control = %q, want no-store", got)
	}
	var fact controlhttp.IntentFact
	decoder := json.NewDecoder(recorder.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		t.Fatalf("decode intent fact: %v", err)
	}
	if fact.Schema != controlhttp.IntentSchema || fact.Repository != "repo_example" {
		t.Fatalf("intent envelope = %#v", fact)
	}
	if fact.Intent != string(accepted.ID) || fact.PreviousIntent != string(accepted.PreviousID) {
		t.Fatalf("intent identity = %#v", fact)
	}
	if fact.Content.Engine != "git" || fact.Content.Revision != accepted.Content.Revision {
		t.Fatalf("intent content = %#v", fact.Content)
	}
}

func TestClientReadsAndValidatesIntentFact(t *testing.T) {
	handler := newHandler(t, &staticService{accepted: intent.Revision{
		ID:      "intent_current",
		Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
	}})
	server := httptest.NewServer(handler)
	defer server.Close()

	fact, err := (controlhttp.Client{Server: server.URL}).Intent(context.Background())
	if err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if fact.Schema != controlhttp.IntentSchema || fact.Intent != "intent_current" {
		t.Fatalf("intent fact = %#v", fact)
	}
}

func TestClientRefusesHTTPRedirects(t *testing.T) {
	target := httptest.NewServer(newHandler(t, &staticService{accepted: intent.Revision{
		ID:      "intent_current",
		Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
	}}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/intent", http.StatusFound)
	}))
	defer redirect.Close()

	_, err := (controlhttp.Client{Server: redirect.URL}).Intent(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("error = %v, want redirect refusal", err)
	}
}

func TestProposalEndpointReturnsADurableVersionReceipt(t *testing.T) {
	service := &staticService{proposed: intent.Proposed{
		Change: intent.Change{ID: "change_one"},
		Version: intent.Version{
			ID:           "version_one",
			ChangeID:     "change_one",
			BaseIntent:   "intent_current",
			Content:      intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
			Producer:     "local:ion",
			Dependencies: []intent.VersionID{"version_parent"},
		},
	}}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodPost, "/v1/proposals", strings.NewReader(`{
		"schema":"grd.proposal/v1",
		"baseIntent":"intent_current",
		"content":{"engine":"git","revision":"0123456789abcdef"},
		"dependencies":["version_parent"]
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "proposal-demo-1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if service.repository != "repo_example" || service.proposal.IdempotencyKey != "proposal-demo-1" {
		t.Fatalf("admission identity = repo %q proposal %#v", service.repository, service.proposal)
	}
	if service.proposal.Producer != "local:ion" || service.proposal.BaseIntent != "intent_current" {
		t.Fatalf("proposal authority = %#v", service.proposal)
	}
	if service.proposal.Content.Engine != "git" || service.proposal.Content.Revision != "0123456789abcdef" {
		t.Fatalf("proposal content = %#v", service.proposal.Content)
	}
	if len(service.proposal.Dependencies) != 1 || service.proposal.Dependencies[0] != "version_parent" {
		t.Fatalf("proposal dependencies = %#v", service.proposal.Dependencies)
	}
	var receipt controlhttp.ProposalReceipt
	decoder := json.NewDecoder(recorder.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("decode proposal receipt: %v", err)
	}
	if receipt.Schema != controlhttp.ProposalReceiptSchema || receipt.Repository != "repo_example" {
		t.Fatalf("receipt envelope = %#v", receipt)
	}
	if receipt.Change.ID != "change_one" || receipt.Version.ID != "version_one" || receipt.Version.Producer != "local:ion" {
		t.Fatalf("receipt identities = %#v", receipt)
	}
	if strings.Contains(recorder.Body.String(), "pending") || strings.Contains(recorder.Body.String(), "state") {
		t.Fatalf("receipt contains mutable status: %s", recorder.Body.String())
	}
}

func TestProposalEndpointMapsIdempotencyConflictWithoutLeakingInternals(t *testing.T) {
	service := &staticService{proposeErr: intent.ErrIdempotencyConflict}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodPost, "/v1/proposals", strings.NewReader(`{
		"schema":"grd.proposal/v1",
		"baseIntent":"intent_current",
		"content":{"engine":"git","revision":"0123456789abcdef"}
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "proposal-demo-1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "already used") {
		t.Fatalf("error = %s, want actionable idempotency conflict", recorder.Body.String())
	}
}

func TestProposalEndpointRejectsDuplicateDependenciesAsCallerInput(t *testing.T) {
	service := &staticService{}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodPost, "/v1/proposals", strings.NewReader(`{
		"schema":"grd.proposal/v1",
		"baseIntent":"intent_current",
		"content":{"engine":"git","revision":"0123456789abcdef"},
		"dependencies":["version_parent","version_parent"]
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "proposal-demo-1")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if service.proposal.IdempotencyKey != "" {
		t.Fatalf("duplicate dependency reached domain service: %#v", service.proposal)
	}
}

func TestClientProposesExactContentWithIdempotency(t *testing.T) {
	service := &staticService{proposed: intent.Proposed{
		Change: intent.Change{ID: "change_one"},
		Version: intent.Version{
			ID:         "version_one",
			ChangeID:   "change_one",
			BaseIntent: "intent_current",
			Content:    intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
			Producer:   "local:ion",
		},
	}}
	server := httptest.NewServer(newHandler(t, service))
	defer server.Close()

	receipt, err := (controlhttp.Client{Server: server.URL}).Propose(context.Background(), "proposal-demo-1", controlhttp.ProposalRequest{
		Schema:     controlhttp.ProposalSchema,
		BaseIntent: "intent_current",
		Content:    controlhttp.Content{Engine: "git", Revision: "0123456789abcdef"},
	})
	if err != nil {
		t.Fatalf("propose content: %v", err)
	}
	if receipt.Schema != controlhttp.ProposalReceiptSchema || receipt.Version.ID != "version_one" {
		t.Fatalf("proposal receipt = %#v", receipt)
	}
}

func TestClientRejectsAReceiptForDifferentProposedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema":"grd.proposal-receipt/v1","repository":"repo_example","change":{"id":"change_one"},"version":{"id":"version_one","change":"change_one","baseIntent":"intent_current","content":{"engine":"git","revision":"different"},"producer":"local:ion"}}`))
	}))
	defer server.Close()

	_, err := (controlhttp.Client{Server: server.URL}).Propose(context.Background(), "proposal-demo-1", controlhttp.ProposalRequest{
		Schema:     controlhttp.ProposalSchema,
		BaseIntent: "intent_current",
		Content:    controlhttp.Content{Engine: "git", Revision: "0123456789abcdef"},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v, want mismatched receipt rejection", err)
	}
}

func TestVersionEndpointProjectsImmutableEvaluationAndPromotionFacts(t *testing.T) {
	service := &staticService{
		version: intent.Version{
			ID:         "version_one",
			ChangeID:   "change_one",
			BaseIntent: "intent_current",
			Content:    intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
			Producer:   "local:ion",
		},
		versionFound: true,
		evaluation: intent.Evaluation{
			VersionID:       "version_one",
			GoverningIntent: "intent_current",
			PolicyEvaluations: []intent.PolicyEvaluation{{
				Policy:      "architecture",
				Instruction: "Keep the design simple?",
				Assignee:    "principal:architecture",
				Reason:      "The change is local.",
				Evidence:    []string{"Only README.md changed."},
				Provenance: intent.EvaluatorProvenance{
					Evaluator:        "example://clear",
					ContractRevision: "example/v1",
				},
			}},
		},
		evaluationFound: true,
		promoted: intent.Promoted{
			Promotion: intent.Promotion{ID: "promotion_one", FromIntent: "intent_current", ToIntent: "intent_next", VersionID: "version_one"},
			Intent:    intent.Revision{ID: "intent_next", PreviousID: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"}},
		},
		promotionFound: true,
	}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodGet, "/v1/versions/version_one", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var inspection controlhttp.VersionInspection
	decoder := json.NewDecoder(recorder.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inspection); err != nil {
		t.Fatalf("decode Version inspection: %v", err)
	}
	if inspection.Schema != controlhttp.VersionSchema || inspection.Version.ID != "version_one" {
		t.Fatalf("Version envelope = %#v", inspection)
	}
	if inspection.Evaluation == nil || inspection.Evaluation.GoverningIntent != "intent_current" || len(inspection.Evaluation.Policies) != 1 {
		t.Fatalf("Evaluation = %#v", inspection.Evaluation)
	}
	if inspection.Evaluation.Policies[0].Provenance.Evaluator != "example://clear" {
		t.Fatalf("Evaluation provenance = %#v", inspection.Evaluation.Policies[0].Provenance)
	}
	if inspection.Promotion == nil || inspection.Promotion.ID != "promotion_one" || inspection.Promotion.ToIntent != "intent_next" {
		t.Fatalf("Promotion = %#v", inspection.Promotion)
	}
	if strings.Contains(recorder.Body.String(), `"state"`) {
		t.Fatalf("inspection contains mutable status: %s", recorder.Body.String())
	}
}

func TestVersionEndpointRefusesPromotionWithoutItsEvaluation(t *testing.T) {
	service := &staticService{
		version:        intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"}, Producer: "local:ion"},
		versionFound:   true,
		promoted:       intent.Promoted{Promotion: intent.Promotion{ID: "promotion_one", FromIntent: "intent_current", ToIntent: "intent_next", VersionID: "version_one"}},
		promotionFound: true,
	}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodGet, "/v1/versions/version_one", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for impossible fact prefix: %s", recorder.Code, recorder.Body.String())
	}
}

func TestVersionEndpointPreservesLatestRequirementResponse(t *testing.T) {
	response := &intent.RequirementResponse{
		ID:        "response_one",
		VersionID: "version_one",
		Policy:    "architecture",
		Assignee:  "principal:architecture",
		Decision:  intent.RequirementRevisionRequested,
		Rationale: "Please keep the public API smaller.",
	}
	service := &staticService{
		version:         intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"}, Producer: "local:ion"},
		versionFound:    true,
		evaluation:      intent.Evaluation{VersionID: "version_one", GoverningIntent: "intent_current", PolicyEvaluations: []intent.PolicyEvaluation{{Policy: "architecture"}}},
		evaluationFound: true,
		requirements: []intent.Requirement{{
			VersionID:      "version_one",
			Policy:         "architecture",
			Assignee:       "principal:architecture",
			Reason:         "The API grew.",
			Evidence:       []string{"A new exported symbol was added."},
			LatestResponse: response,
		}},
	}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodGet, "/v1/versions/version_one", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var inspection controlhttp.VersionInspection
	if err := json.NewDecoder(recorder.Body).Decode(&inspection); err != nil {
		t.Fatalf("decode Version inspection: %v", err)
	}
	if len(inspection.Requirements) != 1 || inspection.Requirements[0].LatestResponse == nil {
		t.Fatalf("Requirements = %#v", inspection.Requirements)
	}
	if inspection.Requirements[0].LatestResponse.Rationale != response.Rationale || inspection.Requirements[0].LatestResponse.Decision != "revision_requested" {
		t.Fatalf("latest Response = %#v", inspection.Requirements[0].LatestResponse)
	}
}

func TestClientInspectsAnExactVersion(t *testing.T) {
	service := &staticService{
		version:      intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"}, Producer: "local:ion"},
		versionFound: true,
	}
	server := httptest.NewServer(newHandler(t, service))
	defer server.Close()

	inspection, err := (controlhttp.Client{Server: server.URL}).Version(context.Background(), "version_one")
	if err != nil {
		t.Fatalf("inspect Version: %v", err)
	}
	if inspection.Schema != controlhttp.VersionSchema || inspection.Version.ID != "version_one" {
		t.Fatalf("Version inspection = %#v", inspection)
	}
}

func TestRequirementsEndpointListsForConfiguredPrincipal(t *testing.T) {
	service := &staticService{}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodGet, "/v1/requirements?limit=25", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if service.pendingRequirementQuery.Assignee != "local:ion" || service.pendingRequirementQuery.Limit != 25 {
		t.Fatalf("pending Requirement query = %#v", service.pendingRequirementQuery)
	}
}

func TestRequirementResponseEndpointUsesConfiguredPrincipal(t *testing.T) {
	service := &staticService{}
	handler := newHandler(t, service)
	body := bytes.NewBufferString(`{"schema":"grd.requirement-response/v1","version":"version_one","policy":"architecture","decision":"satisfied","rationale":"Reviewed the migration."}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/requirement-responses", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "response-one")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if service.requirementResponseRequest.Assignee != "local:ion" {
		t.Fatalf("Response assignee = %q, want configured principal", service.requirementResponseRequest.Assignee)
	}
}

func TestClientPagesRequirementsAndRecordsAnExactResponse(t *testing.T) {
	service := &staticService{
		pendingRequirementPage: intent.PendingRequirementPage{
			Requirements: []intent.Requirement{{
				VersionID: "version_one",
				Policy:    "architecture",
				Assignee:  "local:ion",
				Reason:    "The storage boundary changed.",
				Evidence:  []string{"store.go"},
			}},
			NextCursor: intent.RequirementCursor{VersionID: "version_one", Policy: "architecture"},
		},
	}
	server := httptest.NewServer(newHandler(t, service))
	defer server.Close()
	client := controlhttp.Client{Server: server.URL}

	page, err := client.Requirements(context.Background(), "", 1)
	if err != nil {
		t.Fatalf("list Requirements: %v", err)
	}
	if len(page.Requirements) != 1 || page.Requirements[0].Version != "version_one" || page.NextCursor == "" {
		t.Fatalf("Requirement page = %#v", page)
	}
	request := controlhttp.RequirementResponseRequest{
		Schema:    controlhttp.RequirementResponseSchema,
		Version:   "version_one",
		Policy:    "architecture",
		Decision:  "satisfied",
		Rationale: "Reviewed the storage boundary.",
	}
	receipt, err := client.RespondRequirement(context.Background(), "response-one", request)
	if err != nil {
		t.Fatalf("respond to Requirement: %v", err)
	}
	if receipt.Response.Version != request.Version || receipt.Response.Policy != request.Policy || receipt.Response.Rationale != request.Rationale {
		t.Fatalf("Response receipt = %#v", receipt)
	}
}

func TestHistoryEndpointPagesDurableFacts(t *testing.T) {
	service := &staticService{historyPage: intent.HistoryPage{Facts: []intent.HistoryFact{{Cursor: 1, Kind: intent.HistoryIntentInitialized, Intent: &intent.Revision{ID: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}}}}}}
	handler := newHandler(t, service)
	request := httptest.NewRequest(http.MethodGet, "/v1/history?limit=2", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	if service.historyQuery.Limit != 2 {
		t.Fatalf("history query = %#v", service.historyQuery)
	}
}

func TestHistoryCursorIsBoundToOneLedgerStream(t *testing.T) {
	firstService := &staticService{historyPage: intent.HistoryPage{Facts: []intent.HistoryFact{{Cursor: 1, Kind: intent.HistoryIntentInitialized, Intent: &intent.Revision{ID: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}}}}}}
	firstServer := httptest.NewServer(newHandler(t, firstService))
	defer firstServer.Close()
	first, err := (controlhttp.Client{Server: firstServer.URL}).History(context.Background(), "", 10)
	if err != nil || len(first.Facts) != 1 {
		t.Fatalf("read first history = %#v, %v", first, err)
	}

	secondService := &staticService{historyPage: intent.HistoryPage{Facts: []intent.HistoryFact{{Cursor: 1, Kind: intent.HistoryIntentInitialized, Intent: &intent.Revision{ID: "intent_other", Content: intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}}}}}}
	secondServer := httptest.NewServer(newHandler(t, secondService))
	defer secondServer.Close()
	if _, err := (controlhttp.Client{Server: secondServer.URL}).History(context.Background(), first.Facts[0].Cursor, 10); err == nil {
		t.Fatal("foreign ledger cursor resumed a different history stream")
	}
}

func TestClientRejectsMalformedHistoryFactPayload(t *testing.T) {
	malformed := intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_one", Producer: "local:ion"}
	service := &staticService{historyPage: intent.HistoryPage{Facts: []intent.HistoryFact{
		{Cursor: 1, Kind: intent.HistoryIntentInitialized, Intent: &intent.Revision{ID: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}}},
		{Cursor: 2, Kind: intent.HistoryVersionProposed, Change: &intent.Change{ID: "change_one"}, Version: &malformed},
	}}}
	server := httptest.NewServer(newHandler(t, service))
	defer server.Close()
	if _, err := (controlhttp.Client{Server: server.URL}).History(context.Background(), "", 10); err == nil {
		t.Fatal("client accepted a proposed Version without content")
	}
}

func TestClientReadsAndResumesHistory(t *testing.T) {
	service := &staticService{
		historyPage: intent.HistoryPage{
			Facts: []intent.HistoryFact{{
				Cursor: 1,
				Kind:   intent.HistoryIntentInitialized,
				Intent: &intent.Revision{ID: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}},
			}},
		},
	}
	server := httptest.NewServer(newHandler(t, service))
	defer server.Close()
	client := controlhttp.Client{Server: server.URL}

	page, err := client.History(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("read history: %v", err)
	}
	if len(page.Facts) != 1 || page.Facts[0].Cursor == "" || page.Facts[0].Intent == nil || page.Facts[0].Intent.ID != "intent_one" {
		t.Fatalf("history page = %#v", page)
	}
	if _, err := client.History(context.Background(), page.Facts[0].Cursor, 10); err != nil {
		t.Fatalf("resume history: %v", err)
	}
}

func TestChangeInspectionAndHeldRebaseUseImmutableFacts(t *testing.T) {
	service := &staticService{
		changeInspection: intent.ChangeInspection{
			Change:        intent.Change{ID: "change_one"},
			LatestVersion: intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}, Producer: "local:ion"},
		},
	}
	handler := newHandler(t, service)
	inspectRequest := httptest.NewRequest(http.MethodGet, "/v1/changes/change_one", nil)
	inspectRecorder := httptest.NewRecorder()
	handler.ServeHTTP(inspectRecorder, inspectRequest)
	if inspectRecorder.Code != http.StatusOK {
		t.Fatalf("inspect status = %d, want 200: %s", inspectRecorder.Code, inspectRecorder.Body.String())
	}

	body := bytes.NewBufferString(`{"schema":"grd.held-version-rebase/v1","expectedVersion":"version_one","expectedIntent":"intent_two","content":{"engine":"git","revision":"bbbbbbbb"},"rationale":"Replay onto current Intent."}`)
	rebaseRequest := httptest.NewRequest(http.MethodPost, "/v1/held-version-rebases", body)
	rebaseRequest.Header.Set("Content-Type", "application/json")
	rebaseRequest.Header.Set("Idempotency-Key", "rebase-one")
	rebaseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(rebaseRecorder, rebaseRequest)
	if rebaseRecorder.Code != http.StatusOK {
		t.Fatalf("rebase status = %d, want 200: %s", rebaseRecorder.Code, rebaseRecorder.Body.String())
	}
	if service.heldRebaseRequest.Producer != "local:ion" {
		t.Fatalf("held rebase producer = %q, want configured principal", service.heldRebaseRequest.Producer)
	}
}

func TestClientInspectsChangeAndRecordsExactHeldRebase(t *testing.T) {
	service := &staticService{
		changeInspection: intent.ChangeInspection{
			Change:        intent.Change{ID: "change_one"},
			LatestVersion: intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}, Producer: "local:ion"},
		},
	}
	server := httptest.NewServer(newHandler(t, service))
	defer server.Close()
	client := controlhttp.Client{Server: server.URL}

	change, err := client.Change(context.Background(), "change_one")
	if err != nil || change.LatestVersion.ID != "version_one" {
		t.Fatalf("Change = %#v, error = %v", change, err)
	}
	request := controlhttp.HeldVersionRebaseRequest{Schema: controlhttp.HeldVersionRebaseSchema, ExpectedVersion: "version_one", ExpectedIntent: "intent_two", Content: controlhttp.Content{Engine: "git", Revision: "bbbbbbbb"}, Rationale: "Replay onto current Intent."}
	receipt, err := client.RebaseHeldVersion(context.Background(), "rebase-one", request)
	if err != nil {
		t.Fatalf("rebase held Version: %v", err)
	}
	if receipt.Rebase.FromVersion != request.ExpectedVersion || receipt.Rebase.ToIntent != request.ExpectedIntent || receipt.Version.Content != request.Content {
		t.Fatalf("held rebase receipt = %#v", receipt)
	}
}

func TestClientRejectsAnUnknownIntentSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema":"grd.intent/v2","repository":"repo_example","intent":"intent_current","content":{"engine":"git","revision":"0123456789abcdef"}}`))
	}))
	defer server.Close()

	_, err := (controlhttp.Client{Server: server.URL}).Intent(context.Background())
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unsupported intent schema")) {
		t.Fatalf("error = %v, want unsupported schema", err)
	}
}

func TestClientRejectsTrailingJSONAfterTheIntentFact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"schema":"grd.intent/v1","repository":"repo_example","intent":"intent_current","content":{"engine":"git","revision":"0123456789abcdef"}} {"hidden":true}`))
	}))
	defer server.Close()

	_, err := (controlhttp.Client{Server: server.URL}).Intent(context.Background())
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("single JSON value")) {
		t.Fatalf("error = %v, want trailing JSON rejection", err)
	}
}

func TestClientRejectsANonLoopbackServerBeforeSending(t *testing.T) {
	called := false
	client := controlhttp.Client{
		Server: "http://192.0.2.1:8787",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("unexpected request")
		})},
	}

	_, err := client.Intent(context.Background())
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("loopback")) {
		t.Fatalf("error = %v, want loopback rejection", err)
	}
	if called {
		t.Fatal("client sent a request to a non-loopback server")
	}
}

func newHandler(t *testing.T, service *staticService) http.Handler {
	t.Helper()
	handler, err := controlhttp.NewHandler(controlhttp.Config{
		Repository: "repo_example",
		Producer:   "local:ion",
	}, service)
	if err != nil {
		t.Fatalf("create control handler: %v", err)
	}
	return handler
}

type staticService struct {
	accepted                   intent.Revision
	proposed                   intent.Proposed
	proposeErr                 error
	repository                 string
	proposal                   intentservice.Proposal
	version                    intent.Version
	versionFound               bool
	evaluation                 intent.Evaluation
	evaluationFound            bool
	requirements               []intent.Requirement
	promoted                   intent.Promoted
	promotionFound             bool
	pendingRequirementQuery    intent.PendingRequirementQuery
	requirementResponseRequest intent.RequirementResponseRequest
	pendingRequirementPage     intent.PendingRequirementPage
	historyQuery               intent.HistoryQuery
	historyPage                intent.HistoryPage
	changeInspection           intent.ChangeInspection
	heldRebaseRequest          intentservice.HeldVersionRebaseRequest
}

func (service *staticService) CurrentIntent(context.Context, string) (intent.Revision, error) {
	return service.accepted, nil
}

func (service *staticService) Propose(_ context.Context, repository string, proposal intentservice.Proposal) (intent.Proposed, error) {
	service.repository = repository
	service.proposal = proposal
	return service.proposed, service.proposeErr
}

func (service *staticService) Version(context.Context, string, intent.VersionID) (intent.Version, bool, error) {
	return service.version, service.versionFound, nil
}

func (service *staticService) Evaluation(context.Context, string, intent.VersionID) (intent.Evaluation, bool, error) {
	return service.evaluation, service.evaluationFound, nil
}

func (service *staticService) Requirements(context.Context, string, intent.VersionID) ([]intent.Requirement, error) {
	return service.requirements, nil
}

func (service *staticService) PendingRequirements(_ context.Context, _ string, query intent.PendingRequirementQuery) (intent.PendingRequirementPage, error) {
	service.pendingRequirementQuery = query
	return service.pendingRequirementPage, nil
}

func (service *staticService) RecordRequirementResponse(_ context.Context, _ string, request intent.RequirementResponseRequest) (intent.RequirementResponse, error) {
	service.requirementResponseRequest = request
	return intent.RequirementResponse{
		ID:        "requirement_response_one",
		VersionID: request.VersionID,
		Policy:    request.Policy,
		Assignee:  request.Assignee,
		Decision:  request.Decision,
		Rationale: request.Rationale,
	}, nil
}

func (service *staticService) Promotion(context.Context, string, intent.VersionID) (intent.Promoted, bool, error) {
	return service.promoted, service.promotionFound, nil
}

func (service *staticService) History(_ context.Context, _ string, query intent.HistoryQuery) (intent.HistoryPage, error) {
	service.historyQuery = query
	start := 0
	for index, fact := range service.historyPage.Facts {
		if fact.Cursor == query.After {
			start = index + 1
			break
		}
	}
	end := min(start+query.Limit, len(service.historyPage.Facts))
	page := intent.HistoryPage{Facts: intent.CloneHistoryFacts(service.historyPage.Facts[start:end])}
	if end < len(service.historyPage.Facts) && len(page.Facts) > 0 {
		page.NextCursor = page.Facts[len(page.Facts)-1].Cursor
	}
	return page, nil
}

func (service *staticService) InspectChange(context.Context, string, intent.ChangeID) (intent.ChangeInspection, error) {
	return service.changeInspection, nil
}

func (*staticService) Amend(context.Context, string, intentservice.AmendmentRequest) (intent.Amended, error) {
	return intent.Amended{}, nil
}

func (service *staticService) RebaseHeldVersion(_ context.Context, _ string, request intentservice.HeldVersionRebaseRequest) (intent.RebasedHeldVersion, error) {
	service.heldRebaseRequest = request
	return intent.RebasedHeldVersion{
		Change:  intent.Change{ID: "change_one"},
		Version: intent.Version{ID: "version_two", ChangeID: "change_one", BaseIntent: "intent_two", Content: request.Content, Producer: request.Producer},
		Rebase:  intent.HeldVersionRebase{FromVersion: request.ExpectedVersion, ToVersion: "version_two", FromIntent: "intent_one", ToIntent: request.ExpectedIntent, Rationale: request.Rationale},
	}, nil
}

func (*staticService) ReconcileDependent(context.Context, string, intentservice.DependentReconciliationRequest) (intent.ReconciledDependent, error) {
	return intent.ReconciledDependent{}, nil
}

func (*staticService) RecordReconciliationConflict(context.Context, string, intentservice.ReconciliationConflictRequest) (intent.ReconciliationConflictInspection, error) {
	return intent.ReconciliationConflictInspection{}, nil
}

func (*staticService) ResolveReconciliationConflict(context.Context, string, intentservice.ReconciliationResolutionRequest) (intent.ResolvedReconciliationConflict, error) {
	return intent.ResolvedReconciliationConflict{}, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
