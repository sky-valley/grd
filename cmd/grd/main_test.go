package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/sky-valley/grd/internal/controlhttp"
	"github.com/sky-valley/grd/internal/intent"
)

func TestIntentCommandWritesOneMachineReadableFact(t *testing.T) {
	server := httptest.NewServer(controlhttp.NewHandler("repo_example", commandIntentReader{}))
	defer server.Close()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{"intent", "--server", server.URL}, &stdout, &stderr)

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

func TestIntentCommandRequiresAnExplicitServer(t *testing.T) {
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"intent"}, &bytes.Buffer{}, &stderr); exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("--server")) {
		t.Fatalf("stderr = %q, want server guidance", stderr.String())
	}
}

func TestHelpSucceeds(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := run(context.Background(), []string{"--help"}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("exit code = %d, want 0: %s", exitCode, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("grd intent")) || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

type commandIntentReader struct{}

func (commandIntentReader) CurrentIntent(context.Context, string) (intent.Revision, error) {
	return intent.Revision{
		ID:      "intent_current",
		Content: intent.ContentRef{Engine: "git", Revision: "0123456789abcdef"},
	}, nil
}
