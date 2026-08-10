//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package evaluation_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/grd/internal/evaluation"
	"github.com/sky-valley/grd/internal/evaluatorexec"
	"github.com/sky-valley/grd/internal/evaluatorprotocol"
	"github.com/sky-valley/grd/internal/gitreader"
	"github.com/sky-valley/grd/internal/intent"
)

func TestPolicyProcessorEvaluatesRealGitProposalThroughExternalCommand(t *testing.T) {
	gitDir, acceptedOID, candidateOID := stageEvaluationRepository(t)
	content, err := gitreader.NewSource(realGitLocator{gitDir: gitDir})
	if err != nil {
		t.Fatalf("new Git reader: %v", err)
	}
	inputs, err := evaluation.NewRepositoryEvaluationInputSource(content)
	if err != nil {
		t.Fatalf("new evaluation inputs: %v", err)
	}

	capture := filepath.Join(t.TempDir(), "request.json")
	executable := filepath.Join(t.TempDir(), "evaluator")
	script := `#!/bin/sh
set -eu
IFS= read -r request
printf '%s\n' "$request" > "$GRD_TEST_CAPTURE"
printf '%s\n' '{"schema":"grd.evaluator-result/v1","requiresAction":false,"reason":"architecture remains local","evidence":["app.go changes implementation only"],"provenance":{"evaluator":"example://local","contractRevision":"example/v1"}}'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write evaluator: %v", err)
	}
	external, err := evaluatorexec.New(evaluatorexec.Config{
		Executable:  executable,
		Environment: []string{"GRD_TEST_CAPTURE=" + capture},
	})
	if err != nil {
		t.Fatalf("new external evaluator: %v", err)
	}

	version := intent.Version{
		ID:         "version_real_git",
		ChangeID:   "change_real_git",
		BaseIntent: "intent_accepted",
		Content:    intent.ContentRef{Engine: "git", Revision: candidateOID},
		Producer:   "principal:contributor",
	}
	governing := intent.Revision{
		ID:      "intent_accepted",
		Content: intent.ContentRef{Engine: "git", Revision: acceptedOID},
	}
	service := &recordingPolicyService{context: intent.EvaluationContext{
		Version:         version,
		GoverningIntent: governing,
	}}
	processor, err := evaluation.NewPolicyProcessor(service, external, inputs)
	if err != nil {
		t.Fatalf("new policy processor: %v", err)
	}
	if err := processor.Process(context.Background(), evaluation.WorkItem{
		RepoID:    "repo_real_git",
		VersionID: version.ID,
	}); err != nil {
		t.Fatalf("evaluate real Git proposal: %v", err)
	}

	if service.recorded.VersionID != version.ID || service.recorded.GoverningIntent != governing.ID {
		t.Fatalf("recorded evaluation = %#v", service.recorded)
	}
	if len(service.recorded.PolicyEvaluations) != 1 {
		t.Fatalf("policy evaluations = %#v", service.recorded.PolicyEvaluations)
	}
	recorded := service.recorded.PolicyEvaluations[0]
	if recorded.Policy != "architecture" || recorded.Provenance.Evaluator != "example://local" {
		t.Fatalf("recorded policy evaluation = %#v", recorded)
	}
	if len(service.promotions) != 1 || service.promotions[0] != (intent.PromoteRequest{
		VersionID:      version.ID,
		ExpectedIntent: governing.ID,
	}) {
		t.Fatalf("promotion requests = %#v", service.promotions)
	}

	wire, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read captured evaluator request: %v", err)
	}
	var request evaluatorprotocol.Request
	if err := json.Unmarshal(wire, &request); err != nil {
		t.Fatalf("decode captured evaluator request: %v", err)
	}
	if request.Repository != "repo_real_git" || request.Version != string(version.ID) || request.GoverningIntent != string(governing.ID) {
		t.Fatalf("evaluator identity input = %#v", request)
	}
	if request.Purpose != "Keep storage local and simple.\n" || request.EvaluationPolicy.Name != "architecture" {
		t.Fatalf("evaluator governing input = %#v", request)
	}
	for _, want := range []string{"M\tapp.go", "diff --git a/app.go b/app.go", `+const storage = "postgres"`} {
		if !strings.Contains(request.ChangeEvidence, want) {
			t.Fatalf("change evidence missing %q:\n%s", want, request.ChangeEvidence)
		}
	}
}

type realGitLocator struct {
	gitDir string
}

func (locator realGitLocator) GitDir(context.Context, string) (string, error) {
	return locator.gitDir, nil
}

func stageEvaluationRepository(t *testing.T) (string, string, string) {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runEvaluationGit(t, "init", "-b", "main", repoDir)
	runEvaluationGit(t, "-C", repoDir, "config", "user.name", "GRD Test")
	runEvaluationGit(t, "-C", repoDir, "config", "user.email", "grd@example.invalid")
	writeEvaluationFile(t, filepath.Join(repoDir, ".grd", "purpose.md"), "Keep storage local and simple.\n")
	writeEvaluationFile(t, filepath.Join(repoDir, ".grd", "priorities.md"), "## architecture\nAssignee: principal:architecture\nInstruction: Does this change alter architecture?\n")
	writeEvaluationFile(t, filepath.Join(repoDir, "app.go"), "package app\n\nconst storage = \"memory\"\n")
	runEvaluationGit(t, "-C", repoDir, "add", ".")
	runEvaluationGit(t, "-C", repoDir, "commit", "-m", "accepted")
	accepted := evaluationGitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	writeEvaluationFile(t, filepath.Join(repoDir, "app.go"), "package app\n\nconst storage = \"postgres\"\n")
	runEvaluationGit(t, "-C", repoDir, "add", ".")
	runEvaluationGit(t, "-C", repoDir, "commit", "-m", "candidate")
	candidate := evaluationGitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	return filepath.Join(repoDir, ".git"), accepted, candidate
}

func writeEvaluationFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runEvaluationGit(t *testing.T, args ...string) {
	t.Helper()
	_ = evaluationGitOutput(t, args...)
}

func evaluationGitOutput(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
