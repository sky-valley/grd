package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/grd/internal/controlhttp"
	"github.com/sky-valley/grd/internal/gitworkspace"
	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/intentservice"
)

func TestIntentCommandWritesOneMachineReadableFact(t *testing.T) {
	server := httptest.NewServer(commandHandler(t, &commandService{}))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"intent", "--server", server.URL}, strings.NewReader(""), &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	var fact controlhttp.IntentFact
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if fact.Schema != controlhttp.IntentSchema || fact.Repository != "repo_example" || fact.Intent != "intent_current" {
		t.Fatalf("intent fact = %#v", fact)
	}
}

func TestProposeCommandWritesOneDurableReceipt(t *testing.T) {
	service := &commandService{}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"propose",
		"--server", server.URL,
		"--base-intent", "intent_current",
		"--engine", "git",
		"--revision", "0123456789abcdef",
		"--idempotency-key", "proposal-demo-1",
		"--dependency", "version_parent",
	}, strings.NewReader(""), &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	var receipt controlhttp.ProposalReceipt
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if receipt.Schema != controlhttp.ProposalReceiptSchema || receipt.Change.ID != "change_one" || receipt.Version.ID != "version_one" {
		t.Fatalf("proposal receipt = %#v", receipt)
	}
	if service.proposal.IdempotencyKey != "proposal-demo-1" || len(service.proposal.Dependencies) != 1 || service.proposal.Dependencies[0] != "version_parent" {
		t.Fatalf("admitted proposal = %#v", service.proposal)
	}
}

func TestProposeCommandReadsVersionedJSONFromStdin(t *testing.T) {
	service := &commandService{}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	input := strings.NewReader(`{"schema":"grd.proposal/v1","baseIntent":"intent_current","content":{"engine":"git","revision":"0123456789abcdef"}}`)

	exitCode := run(context.Background(), []string{
		"propose",
		"--server", server.URL,
		"--idempotency-key", "proposal-stdin-1",
		"--input", "-",
	}, input, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	if service.proposal.BaseIntent != "intent_current" || service.proposal.Content.Revision != "0123456789abcdef" {
		t.Fatalf("admitted proposal = %#v", service.proposal)
	}
}

func TestVersionCommandWritesImmutableFacts(t *testing.T) {
	service := &commandService{}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"version", "--server", server.URL, "--id", "version_one"}, strings.NewReader(""), &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	var inspection controlhttp.VersionInspection
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inspection); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, stdout.String())
	}
	if inspection.Schema != controlhttp.VersionSchema || inspection.Version.ID != "version_one" {
		t.Fatalf("Version inspection = %#v", inspection)
	}
}

func TestRequirementsCommandPrintsAResumablePage(t *testing.T) {
	service := &commandService{
		pendingRequirementPage: intent.PendingRequirementPage{
			Requirements: []intent.Requirement{{
				VersionID: "version_one",
				Policy:    "architecture",
				Assignee:  "local:ion",
				Reason:    "The storage boundary changed.",
				Evidence:  []string{"store.go"},
			}},
		},
	}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"requirements", "--server", server.URL, "--limit", "10"}, strings.NewReader(""), &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	var page controlhttp.RequirementsPage
	if err := json.Unmarshal(stdout.Bytes(), &page); err != nil {
		t.Fatalf("decode Requirement page: %v", err)
	}
	if len(page.Requirements) != 1 || page.Requirements[0].Version != "version_one" {
		t.Fatalf("Requirement page = %#v", page)
	}
}

func TestRespondCommandReadsAgentNativeJSONFromStdin(t *testing.T) {
	service := &commandService{}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	input := strings.NewReader(`{"schema":"grd.requirement-response/v1","version":"version_one","policy":"architecture","decision":"satisfied","rationale":"Reviewed the storage boundary."}`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"respond", "--server", server.URL, "--idempotency-key", "response-one", "--input", "-"}, input, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	if service.requirementResponseRequest.VersionID != "version_one" || service.requirementResponseRequest.Assignee != "local:ion" {
		t.Fatalf("Requirement Response request = %#v", service.requirementResponseRequest)
	}
	var receipt controlhttp.RequirementResponseReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.Response.ID == "" {
		t.Fatalf("Response receipt = %#v, decode error = %v", receipt, err)
	}
}

func TestHistoryCommandPrintsOneCursorablePage(t *testing.T) {
	service := &commandService{historyPage: commandHistoryPage()}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"history", "--server", server.URL, "--limit", "10"}, strings.NewReader(""), &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	var page controlhttp.HistoryPage
	if err := json.Unmarshal(stdout.Bytes(), &page); err != nil || len(page.Facts) != 1 || page.Facts[0].Cursor == "" {
		t.Fatalf("history page = %#v, decode error = %v", page, err)
	}
}

func TestWatchCommandEmitsResumableJSONL(t *testing.T) {
	service := &commandService{historyPage: commandHistoryPage()}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	stdout := &cancelWriter{cancel: cancel}
	var stderr bytes.Buffer

	exitCode := run(ctx, []string{"watch", "--server", server.URL, "--poll-interval", "1ms"}, strings.NewReader(""), stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	var envelope controlhttp.HistoryFactEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil || envelope.Schema != controlhttp.HistoryFactSchema || envelope.Repository != "repo_example" || envelope.Fact.Cursor == "" || envelope.Fact.Kind != "intent_initialized" {
		t.Fatalf("watched fact = %#v, decode error = %v", envelope, err)
	}
}

func TestHistoryHelpSucceeds(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"history", "--help"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
}

func TestSubmitCommandAdmitsCurrentGitHead(t *testing.T) {
	workdir, accepted, head := commandGitFixture(t)
	service := &commandService{accepted: intent.Revision{ID: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: accepted}}}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"submit", "--server", server.URL, "--workdir", workdir}, strings.NewReader(""), &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	if service.proposal.Content.Revision != head || service.proposal.BaseIntent != "intent_current" {
		t.Fatalf("submitted proposal = %#v", service.proposal)
	}
	var receipt controlhttp.ProposalReceipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.Version.Content.Revision != head {
		t.Fatalf("submit receipt = %#v, decode error = %v", receipt, err)
	}
}

func TestStatusCommandDerivesWorkspaceRelationship(t *testing.T) {
	workdir, accepted, head := commandGitFixture(t)
	version := intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: head}, Producer: "local:ion"}
	service := &commandService{
		accepted: intent.Revision{ID: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: accepted}},
		historyPage: intent.HistoryPage{Facts: []intent.HistoryFact{
			{Cursor: 1, Kind: intent.HistoryIntentInitialized, Intent: &intent.Revision{ID: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: accepted}}},
			{Cursor: 2, Kind: intent.HistoryVersionProposed, Change: &intent.Change{ID: "change_one"}, Version: &version},
		}},
		version:          version,
		changeInspection: intent.ChangeInspection{Change: intent.Change{ID: "change_one"}, LatestVersion: version},
	}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"status", "--server", server.URL, "--workdir", workdir}, strings.NewReader(""), &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	var fact gitworkspace.StatusFact
	if err := json.Unmarshal(stdout.Bytes(), &fact); err != nil || fact.Relation != gitworkspace.RelationProposed || fact.Head.Revision != head {
		t.Fatalf("workspace fact = %#v, decode error = %v", fact, err)
	}
}

func TestSyncCommandRebasesAndRecordsReplacementVersion(t *testing.T) {
	workdir, accepted, head := commandGitFixture(t)
	commandGitRun(t, workdir, "switch", "-c", "accepted-update", accepted)
	if err := os.WriteFile(filepath.Join(workdir, "accepted.txt"), []byte("accepted update\n"), 0o600); err != nil {
		t.Fatalf("write accepted update: %v", err)
	}
	commandGitRun(t, workdir, "add", "accepted.txt")
	commandGitRun(t, workdir, "commit", "-m", "advance accepted Intent")
	current := commandGitOutput(t, workdir, "rev-parse", "HEAD")
	commandGitRun(t, workdir, "switch", "-C", "candidate", head)
	version := intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: head}, Producer: "local:ion"}
	initialIntent := intent.Revision{ID: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: accepted}}
	change := intent.Change{ID: "change_one"}
	service := &commandService{}
	service.accepted = intent.Revision{ID: "intent_two", PreviousID: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: current}}
	service.historyPage = intent.HistoryPage{Facts: []intent.HistoryFact{
		{Cursor: 1, Kind: intent.HistoryIntentInitialized, Intent: &initialIntent},
		{Cursor: 2, Kind: intent.HistoryVersionProposed, Change: &change, Version: &version},
	}}
	service.changeInspection = intent.ChangeInspection{Change: change, LatestVersion: version}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"sync", "--server", server.URL, "--workdir", workdir, "--version", "version_one", "--rationale", "Replay onto current Intent."}, strings.NewReader(""), &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	var fact gitworkspace.SyncFact
	if err := json.Unmarshal(stdout.Bytes(), &fact); err != nil || fact.Action != "held_version_rebased" || fact.ToVersion == "" {
		t.Fatalf("sync fact = %#v, decode error = %v", fact, err)
	}
}

func TestChangeAndRebaseHeldCommandsExposeExplicitFacts(t *testing.T) {
	version := intent.Version{ID: "version_one", ChangeID: "change_one", BaseIntent: "intent_one", Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Producer: "local:ion"}
	service := &commandService{changeInspection: intent.ChangeInspection{Change: intent.Change{ID: "change_one"}, LatestVersion: version}}
	server := httptest.NewServer(commandHandler(t, service))
	defer server.Close()
	var changeOut bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"change", "--server", server.URL, "--id", "change_one"}, strings.NewReader(""), &changeOut, &stderr); exitCode != 0 {
		t.Fatalf("change exit code = %d: %s", exitCode, stderr.String())
	}
	var change controlhttp.ChangeInspection
	if err := json.Unmarshal(changeOut.Bytes(), &change); err != nil || change.Change.ID != "change_one" {
		t.Fatalf("Change inspection = %#v, decode error = %v", change, err)
	}

	input := strings.NewReader(`{"schema":"grd.held-version-rebase/v1","expectedVersion":"version_one","expectedIntent":"intent_two","content":{"engine":"git","revision":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"rationale":"Replay onto current Intent."}`)
	var receiptOut bytes.Buffer
	stderr.Reset()
	if exitCode := run(context.Background(), []string{"rebase-held", "--server", server.URL, "--idempotency-key", "rebase-one", "--input", "-"}, input, &receiptOut, &stderr); exitCode != 0 {
		t.Fatalf("rebase-held exit code = %d: %s", exitCode, stderr.String())
	}
	var receipt controlhttp.HeldVersionRebaseReceipt
	if err := json.Unmarshal(receiptOut.Bytes(), &receipt); err != nil || receipt.Version.ID == "" {
		t.Fatalf("held rebase receipt = %#v, decode error = %v", receipt, err)
	}
}

func TestIntentCommandRequiresAnExplicitServer(t *testing.T) {
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"intent"}, strings.NewReader(""), &bytes.Buffer{}, &stderr); exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("--server")) {
		t.Fatalf("stderr = %q, want server guidance", stderr.String())
	}
}

func TestHelpSucceeds(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"--help"}, strings.NewReader(""), &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("grd intent")) || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func commandHandler(t *testing.T, service *commandService) http.Handler {
	t.Helper()
	handler, err := controlhttp.NewHandler(controlhttp.Config{Repository: "repo_example", Producer: "local:ion"}, service)
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}
	return handler
}

type commandService struct {
	proposal                   intentservice.Proposal
	pendingRequirementPage     intent.PendingRequirementPage
	requirementResponseRequest intent.RequirementResponseRequest
	historyPage                intent.HistoryPage
	historyQueries             []intent.HistoryQuery
	accepted                   intent.Revision
	version                    intent.Version
	changeInspection           intent.ChangeInspection
	heldRebaseRequest          intentservice.HeldVersionRebaseRequest
}

func (service *commandService) CurrentIntent(context.Context, string) (intent.Revision, error) {
	if service.accepted.ID != "" {
		return service.accepted, nil
	}
	return intent.Revision{
		ID:      "intent_current",
		Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
	}, nil
}

func (service *commandService) Propose(_ context.Context, _ string, proposal intentservice.Proposal) (intent.Proposed, error) {
	service.proposal = proposal
	return intent.Proposed{
		Change: intent.Change{ID: "change_one"},
		Version: intent.Version{
			ID:           "version_one",
			ChangeID:     "change_one",
			BaseIntent:   proposal.BaseIntent,
			Content:      proposal.Content,
			Producer:     proposal.Producer,
			Dependencies: proposal.Dependencies,
		},
	}, nil
}

func (service *commandService) Version(context.Context, string, intent.VersionID) (intent.Version, bool, error) {
	if service.version.ID != "" {
		return service.version, true, nil
	}
	return intent.Version{
		ID:         "version_one",
		ChangeID:   "change_one",
		BaseIntent: "intent_current",
		Content:    intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
		Producer:   "local:ion",
	}, true, nil
}

func (*commandService) Evaluation(context.Context, string, intent.VersionID) (intent.Evaluation, bool, error) {
	return intent.Evaluation{}, false, nil
}

func (*commandService) Requirements(context.Context, string, intent.VersionID) ([]intent.Requirement, error) {
	return nil, nil
}

func (service *commandService) PendingRequirements(context.Context, string, intent.PendingRequirementQuery) (intent.PendingRequirementPage, error) {
	return service.pendingRequirementPage, nil
}

func (service *commandService) RecordRequirementResponse(_ context.Context, _ string, request intent.RequirementResponseRequest) (intent.RequirementResponse, error) {
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

func (*commandService) Promotion(context.Context, string, intent.VersionID) (intent.Promoted, bool, error) {
	return intent.Promoted{}, false, nil
}

func (service *commandService) History(_ context.Context, _ string, query intent.HistoryQuery) (intent.HistoryPage, error) {
	service.historyQueries = append(service.historyQueries, query)
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

func (service *commandService) InspectChange(context.Context, string, intent.ChangeID) (intent.ChangeInspection, error) {
	return service.changeInspection, nil
}

func (*commandService) Amend(context.Context, string, intentservice.AmendmentRequest) (intent.Amended, error) {
	return intent.Amended{}, nil
}

func (service *commandService) RebaseHeldVersion(_ context.Context, _ string, request intentservice.HeldVersionRebaseRequest) (intent.RebasedHeldVersion, error) {
	service.heldRebaseRequest = request
	return intent.RebasedHeldVersion{
		Change:  intent.Change{ID: service.changeInspection.Change.ID},
		Version: intent.Version{ID: "version_two", ChangeID: service.changeInspection.Change.ID, BaseIntent: request.ExpectedIntent, Content: request.Content, Producer: request.Producer},
		Rebase:  intent.HeldVersionRebase{FromVersion: request.ExpectedVersion, ToVersion: "version_two", FromIntent: service.changeInspection.LatestVersion.BaseIntent, ToIntent: request.ExpectedIntent, Rationale: request.Rationale},
	}, nil
}

func (*commandService) ReconcileDependent(context.Context, string, intentservice.DependentReconciliationRequest) (intent.ReconciledDependent, error) {
	return intent.ReconciledDependent{}, nil
}

func (*commandService) RecordReconciliationConflict(context.Context, string, intentservice.ReconciliationConflictRequest) (intent.ReconciliationConflictInspection, error) {
	return intent.ReconciliationConflictInspection{}, nil
}

func (*commandService) ResolveReconciliationConflict(context.Context, string, intentservice.ReconciliationResolutionRequest) (intent.ResolvedReconciliationConflict, error) {
	return intent.ResolvedReconciliationConflict{}, nil
}

func commandHistoryPage() intent.HistoryPage {
	return intent.HistoryPage{Facts: []intent.HistoryFact{{
		Cursor: 1,
		Kind:   intent.HistoryIntentInitialized,
		Intent: &intent.Revision{ID: "intent_current", Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"}},
	}}}
}

type cancelWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func commandGitFixture(t *testing.T) (string, string, string) {
	t.Helper()
	workdir := t.TempDir()
	commandGitRun(t, workdir, "init", "-b", "main")
	commandGitRun(t, workdir, "config", "user.name", "GRD Test")
	commandGitRun(t, workdir, "config", "user.email", "grd@example.invalid")
	if err := os.WriteFile(filepath.Join(workdir, "README.md"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatalf("write accepted file: %v", err)
	}
	commandGitRun(t, workdir, "add", "README.md")
	commandGitRun(t, workdir, "commit", "-m", "accepted")
	accepted := commandGitOutput(t, workdir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(workdir, "README.md"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}
	commandGitRun(t, workdir, "add", "README.md")
	commandGitRun(t, workdir, "commit", "-m", "candidate")
	return workdir, accepted, commandGitOutput(t, workdir, "rev-parse", "HEAD")
}

func commandGitRun(t *testing.T, workdir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", workdir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func commandGitOutput(t *testing.T, workdir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", workdir}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

func (writer *cancelWriter) Write(value []byte) (int, error) {
	written, err := writer.Buffer.Write(value)
	writer.cancel()
	return written, err
}
