//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky-valley/grd/internal/evaluation"
	"github.com/sky-valley/grd/internal/evaluatorexec"
	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/intentservice"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestSingleRepositoryRuntimeEvaluatesPromotesAndRestarts(t *testing.T) {
	gitDir, acceptedOID, candidateOID := stageRuntimeRepository(t)
	ledgerPath := filepath.Join(t.TempDir(), "state", "decision-loop.jsonl")
	evaluator := writeClearEvaluator(t)
	reports := make(chan error, 8)
	config := singleRepositoryConfig{
		RepositoryID: "repo_example",
		GitDir:       gitDir,
		LedgerPath:   ledgerPath,
		TrunkRef:     "refs/heads/main",
		Evaluator:    evaluatorexec.Config{Executable: evaluator},
		Runner: evaluation.RunnerOptions{
			Workers:         1,
			BatchSize:       1,
			PollInterval:    5 * time.Millisecond,
			LeaseTTL:        time.Second,
			RetryBackoff:    5 * time.Millisecond,
			MaxRetryBackoff: 20 * time.Millisecond,
			Report: func(err error) {
				reports <- err
			},
		},
	}

	runtime, err := openSingleRepository(context.Background(), config)
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	current, err := runtime.Service().CurrentIntent(context.Background(), config.RepositoryID)
	if err != nil {
		t.Fatalf("read initial Intent: %v", err)
	}
	if current.Content != (intent.ContentRef{Engine: "git", Revision: acceptedOID}) {
		t.Fatalf("initial content = %#v", current.Content)
	}
	if _, err := openSingleRepository(context.Background(), config); err == nil {
		t.Fatal("second runtime acquired the same ledger lock")
	}

	proposed, err := runtime.Service().Propose(context.Background(), config.RepositoryID, intentservice.Proposal{
		IdempotencyKey: "proposal-1",
		BaseIntent:     current.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: candidateOID},
		Producer:       "principal:contributor",
	})
	if err != nil {
		t.Fatalf("propose candidate: %v", err)
	}

	runContext, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		runtime.Run(runContext)
	}()

	waitForRuntime(t, 5*time.Second, func() bool {
		current, err := runtime.Service().CurrentIntent(context.Background(), config.RepositoryID)
		return err == nil && current.Content.Revision == candidateOID
	})
	cancelRun()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	select {
	case err := <-reports:
		t.Fatalf("runtime reported an error: %v", err)
	default:
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}

	restarted, err := openSingleRepository(context.Background(), config)
	if err != nil {
		t.Fatalf("restart runtime: %v", err)
	}
	defer func() {
		if err := restarted.Close(); err != nil {
			t.Errorf("close restarted runtime: %v", err)
		}
	}()
	restartedCurrent, err := restarted.Service().CurrentIntent(context.Background(), config.RepositoryID)
	if err != nil {
		t.Fatalf("read restarted Intent: %v", err)
	}
	if restartedCurrent.Content.Revision != candidateOID {
		t.Fatalf("restarted content = %q, want %q", restartedCurrent.Content.Revision, candidateOID)
	}
	recorded, found, err := restarted.Service().Evaluation(context.Background(), config.RepositoryID, proposed.Version.ID)
	if err != nil {
		t.Fatalf("read restarted evaluation: %v", err)
	}
	if !found || recorded.VersionID != proposed.Version.ID || len(recorded.PolicyEvaluations) != 1 {
		t.Fatalf("restarted evaluation = %#v, found %t", recorded, found)
	}
	pending, err := restarted.Service().RunnableEvaluations(context.Background(), config.RepositoryID, intent.PendingEvaluationQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list restarted pending evaluations: %v", err)
	}
	if len(pending.Versions) != 0 {
		t.Fatalf("restarted pending evaluations = %#v", pending.Versions)
	}
}

func TestSingleRepositoryRuntimeRejectsDivergedGitTrunkAndReleasesLedger(t *testing.T) {
	gitDir, acceptedOID, candidateOID := stageRuntimeRepository(t)
	config := singleRepositoryConfig{
		RepositoryID: "repo_example",
		GitDir:       gitDir,
		LedgerPath:   filepath.Join(t.TempDir(), "state", "decision-loop.jsonl"),
		TrunkRef:     "refs/heads/main",
		Evaluator:    evaluatorexec.Config{Executable: writeClearEvaluator(t)},
	}
	runtime, err := openSingleRepository(context.Background(), config)
	if err != nil {
		t.Fatalf("initialize runtime: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("close initialized runtime: %v", err)
	}
	runRuntimeGit(t, "--git-dir", gitDir, "update-ref", "refs/heads/main", candidateOID, acceptedOID)

	unexpected, err := openSingleRepository(context.Background(), config)
	if err == nil {
		_ = unexpected.Close()
		t.Fatal("runtime accepted a Git trunk that diverged from durable Intent")
	}
	runRuntimeGit(t, "--git-dir", gitDir, "update-ref", "refs/heads/main", acceptedOID, candidateOID)
	reopened, err := openSingleRepository(context.Background(), config)
	if err != nil {
		t.Fatalf("reopen after restoring trunk: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened runtime: %v", err)
	}
}

func TestSingleRepositoryRuntimeRejectsPendingPromotionConflictAndReleasesLedger(t *testing.T) {
	gitDir, acceptedOID, candidateOID := stageRuntimeRepository(t)
	divergentOID := stageDivergentRuntimeCommit(t, gitDir, acceptedOID)
	ledgerPath := filepath.Join(t.TempDir(), "state", "decision-loop.jsonl")
	if err := os.MkdirAll(filepath.Dir(ledgerPath), 0o700); err != nil {
		t.Fatalf("create ledger directory: %v", err)
	}
	ledger, err := ledgerfs.Open(ledgerPath)
	if err != nil {
		t.Fatalf("open seed ledger: %v", err)
	}
	projection := &divergingRuntimeProjection{
		current:   intent.ContentRef{Engine: "git", Revision: acceptedOID},
		divergent: intent.ContentRef{Engine: "git", Revision: divergentOID},
	}
	repository, err := intent.OpenRepository(
		context.Background(),
		projection.current,
		ledger,
		runtimeAdmission{},
		projection,
	)
	if err != nil {
		t.Fatalf("open seed repository: %v", err)
	}
	current := repository.CurrentIntent()
	proposed, err := repository.Propose(context.Background(), intent.Proposal{
		IdempotencyKey: "pending-promotion",
		BaseIntent:     current.ID,
		Content:        intent.ContentRef{Engine: "git", Revision: candidateOID},
		Producer:       "principal:contributor",
	})
	if err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	_, err = repository.RecordEvaluation(context.Background(), intent.Evaluation{
		VersionID:       proposed.Version.ID,
		GoverningIntent: current.ID,
		PolicyEvaluations: []intent.PolicyEvaluation{{
			Policy:         "architecture",
			Instruction:    "Does this preserve the repository architecture?",
			Assignee:       "principal:architecture",
			RequiresAction: false,
			Reason:         "the candidate preserves the accepted architecture",
			Evidence:       []string{"the change is limited to app.txt"},
		}},
	})
	if err != nil {
		t.Fatalf("seed evaluation: %v", err)
	}
	_, err = repository.Promote(context.Background(), intent.PromoteRequest{
		VersionID:      proposed.Version.ID,
		ExpectedIntent: current.ID,
	})
	var conflict *intent.ProjectionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("seed promotion error = %v, want projection conflict", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close seed ledger: %v", err)
	}
	runRuntimeGit(t, "--git-dir", gitDir, "update-ref", "refs/heads/main", divergentOID, acceptedOID)
	config := singleRepositoryConfig{
		RepositoryID: "repo_example",
		GitDir:       gitDir,
		LedgerPath:   ledgerPath,
		TrunkRef:     "refs/heads/main",
		Evaluator:    evaluatorexec.Config{Executable: writeClearEvaluator(t)},
	}

	unexpected, err := openSingleRepository(context.Background(), config)
	if err == nil {
		_ = unexpected.Close()
		t.Fatal("runtime accepted an unresolved pending-promotion conflict")
	}
	runRuntimeGit(t, "--git-dir", gitDir, "update-ref", "refs/heads/main", acceptedOID, divergentOID)
	recovered, err := openSingleRepository(context.Background(), config)
	if err != nil {
		t.Fatalf("reopen after restoring promotion base: %v", err)
	}
	defer func() {
		if err := recovered.Close(); err != nil {
			t.Errorf("close recovered runtime: %v", err)
		}
	}()
	recoveredIntent, err := recovered.Service().CurrentIntent(context.Background(), config.RepositoryID)
	if err != nil {
		t.Fatalf("read recovered Intent: %v", err)
	}
	if recoveredIntent.Content.Revision != candidateOID {
		t.Fatalf("recovered content = %q, want %q", recoveredIntent.Content.Revision, candidateOID)
	}
}

func stageRuntimeRepository(t *testing.T) (gitDir, acceptedOID, candidateOID string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repo")
	runRuntimeGit(t, "init", "-b", "main", repository)
	runRuntimeGit(t, "-C", repository, "config", "user.name", "GRD Test")
	runRuntimeGit(t, "-C", repository, "config", "user.email", "grd@example.invalid")
	writeRuntimeFile(t, filepath.Join(repository, ".grd", "purpose.md"), "Keep the example small and dependable.\n")
	writeRuntimeFile(t, filepath.Join(repository, ".grd", "priorities.md"), "## architecture\nAssignee: principal:architecture\nInstruction: Does this preserve the repository architecture?\n")
	writeRuntimeFile(t, filepath.Join(repository, "app.txt"), "accepted\n")
	runRuntimeGit(t, "-C", repository, "add", ".")
	runRuntimeGit(t, "-C", repository, "commit", "-m", "accepted")
	acceptedOID = runtimeGitOutput(t, "-C", repository, "rev-parse", "HEAD")
	writeRuntimeFile(t, filepath.Join(repository, "app.txt"), "candidate\n")
	runRuntimeGit(t, "-C", repository, "add", ".")
	runRuntimeGit(t, "-C", repository, "commit", "-m", "candidate")
	candidateOID = runtimeGitOutput(t, "-C", repository, "rev-parse", "HEAD")
	runRuntimeGit(t, "-C", repository, "reset", "--hard", acceptedOID)
	return filepath.Join(repository, ".git"), acceptedOID, candidateOID
}

func writeClearEvaluator(t *testing.T) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "clear-evaluator")
	script := `#!/bin/sh
set -eu
IFS= read -r request
test -n "$request"
printf '%s\n' '{"schema":"grd.evaluator-result/v1","requiresAction":false,"reason":"the candidate preserves the accepted architecture","evidence":["the change is limited to app.txt"],"provenance":{"evaluator":"example://clear","contractRevision":"example/v1"}}'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatalf("write evaluator: %v", err)
	}
	return executable
}

func stageDivergentRuntimeCommit(t *testing.T, gitDir, acceptedOID string) string {
	t.Helper()
	repository := filepath.Dir(gitDir)
	writeRuntimeFile(t, filepath.Join(repository, "app.txt"), "divergent\n")
	runRuntimeGit(t, "-C", repository, "add", "app.txt")
	runRuntimeGit(t, "-C", repository, "commit", "-m", "divergent")
	divergentOID := runtimeGitOutput(t, "-C", repository, "rev-parse", "HEAD")
	runRuntimeGit(t, "-C", repository, "reset", "--hard", acceptedOID)
	return divergentOID
}

type runtimeAdmission struct{}

func (runtimeAdmission) Admit(context.Context, intent.VersionID, intent.ContentRef) error {
	return nil
}

type divergingRuntimeProjection struct {
	current   intent.ContentRef
	divergent intent.ContentRef
}

func (projection *divergingRuntimeProjection) Current(context.Context) (intent.ContentRef, error) {
	return projection.current, nil
}

func (projection *divergingRuntimeProjection) Advance(context.Context, intent.ContentRef, intent.ContentRef) error {
	projection.current = projection.divergent
	return intent.ErrIntentAdvanced
}

func writeRuntimeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runRuntimeGit(t *testing.T, args ...string) {
	t.Helper()
	_ = runtimeGitOutput(t, args...)
}

func runtimeGitOutput(t *testing.T, args ...string) string {
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

func waitForRuntime(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("runtime condition was not satisfied")
}
