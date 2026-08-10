package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sky-valley/grd/internal/controlhttp"
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
	proposal intentservice.Proposal
}

func (*commandService) CurrentIntent(context.Context, string) (intent.Revision, error) {
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

func (*commandService) Version(context.Context, string, intent.VersionID) (intent.Version, bool, error) {
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

func (*commandService) Promotion(context.Context, string, intent.VersionID) (intent.Promoted, bool, error) {
	return intent.Promoted{}, false, nil
}
