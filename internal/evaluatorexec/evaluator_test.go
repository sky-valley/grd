//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package evaluatorexec_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky-valley/grd/internal/evaluation"
	"github.com/sky-valley/grd/internal/evaluatorexec"
	"github.com/sky-valley/grd/internal/intent"
)

func TestEvaluatorUsesProviderNeutralProcessContract(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "request.json")
	captureCWD := filepath.Join(t.TempDir(), "cwd.txt")
	t.Setenv("GRD_TEST_PARENT_SECRET", "must-not-leak")
	executable := writeEvaluator(t, `#!/bin/sh
set -eu
if [ "${GRD_TEST_PARENT_SECRET-unset}" != "unset" ]; then
  printf 'inherited parent secret' >&2
  exit 9
fi
printf '%s\n' "$PWD" > "$GRD_TEST_EVALUATOR_CWD"
IFS= read -r request
printf '%s\n' "$request" > "$GRD_TEST_EVALUATOR_CAPTURE"
printf '%s\n' '{"schema":"grd.evaluator-result/v1","requiresAction":true,"reason":"architecture changed","evidence":["internal/store.go"],"provenance":{"evaluator":"example://deterministic","contractRevision":"example/v1"}}'
`)
	evaluator, err := evaluatorexec.New(evaluatorexec.Config{
		Executable: executable,
		Environment: []string{
			"GRD_TEST_EVALUATOR_CAPTURE=" + capture,
			"GRD_TEST_EVALUATOR_CWD=" + captureCWD,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.Evaluate(context.Background(), evaluation.EvaluationRequest{
		RepoID:          "repo_example",
		Version:         intent.Version{ID: "version_candidate"},
		GoverningIntent: intent.Revision{ID: "intent_current"},
		Policy: evaluation.Policy{
			Name:        "architecture",
			Instruction: "Does architecture change?",
			Assignee:    "principal:architecture",
		},
		Purpose:        "example purpose",
		Priorities:     "example priorities",
		ChangeEvidence: "M internal/store.go",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !result.RequiresAction || result.Reason != "architecture changed" || len(result.Evidence) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Provenance.Evaluator != "example://deterministic" || result.Provenance.ContractRevision != "example/v1" {
		t.Fatalf("provenance = %#v", result.Provenance)
	}
	workingDirectory, err := os.ReadFile(captureCWD)
	if err != nil {
		t.Fatal(err)
	}
	gotDirectory := strings.TrimSpace(string(workingDirectory))
	gotDirectoryInfo, err := os.Stat(gotDirectory)
	if err != nil {
		t.Fatal(err)
	}
	wantDirectoryInfo, err := os.Stat(filepath.Dir(executable))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotDirectoryInfo, wantDirectoryInfo) {
		t.Fatalf("evaluator working directory = %q, want executable directory %q", workingDirectory, filepath.Dir(executable))
	}

	wire, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	text := string(wire)
	for _, want := range []string{
		`"schema":"grd.evaluator-request/v1"`,
		`"instruction":"Does architecture change?"`,
		`"assignee":"principal:architecture"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("request missing %s: %s", want, text)
		}
	}
}

func TestEvaluatorReturnsBoundedProcessFailure(t *testing.T) {
	executable := writeEvaluator(t, "#!/bin/sh\ni=0\nwhile [ $i -lt 100000 ]; do printf x >&2; i=$((i + 1)); done\nexit 7\n")
	evaluator, err := evaluatorexec.New(evaluatorexec.Config{Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	_, err = evaluator.Evaluate(context.Background(), evaluation.EvaluationRequest{})
	if err == nil {
		t.Fatal("failing evaluator succeeded")
	}
	if len(err.Error()) > 5*1024 {
		t.Fatalf("evaluator diagnostic length = %d, want bounded error", len(err.Error()))
	}
}

func TestEvaluatorRejectsInvalidResultStreams(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "unknown field",
			output: `{"schema":"grd.evaluator-result/v1","requiresAction":false,"reason":"clear","evidence":["none"],"provenance":{"evaluator":"example://test","contractRevision":"v1"},"status":"clear"}`,
			want:   "unknown field",
		},
		{
			name:   "second value",
			output: `{"schema":"grd.evaluator-result/v1","requiresAction":false,"reason":"clear","evidence":["none"],"provenance":{"evaluator":"example://test","contractRevision":"v1"}} {}`,
			want:   "more than one JSON value",
		},
		{
			name:   "wrong schema",
			output: `{"schema":"grd.evaluator-result/v2","requiresAction":false,"reason":"clear","evidence":["none"],"provenance":{"evaluator":"example://test","contractRevision":"v1"}}`,
			want:   "want \"grd.evaluator-result/v1\"",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable := writeEvaluator(t, "#!/bin/sh\nprintf '%s\\n' '"+test.output+"'\n")
			evaluator, err := evaluatorexec.New(evaluatorexec.Config{Executable: executable})
			if err != nil {
				t.Fatal(err)
			}
			_, err = evaluator.Evaluate(context.Background(), evaluation.EvaluationRequest{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("result stream error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEvaluatorRejectsOversizedRequestBeforeStartingProcess(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	executable := writeEvaluator(t, "#!/bin/sh\n: > \"$GRD_TEST_EVALUATOR_STARTED\"\n")
	evaluator, err := evaluatorexec.New(evaluatorexec.Config{
		Executable:  executable,
		Environment: []string{"GRD_TEST_EVALUATOR_STARTED=" + started},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = evaluator.Evaluate(context.Background(), evaluation.EvaluationRequest{
		ChangeEvidence: strings.Repeat("x", 5*1024*1024),
	})
	if err == nil || !strings.Contains(err.Error(), "request exceeds") {
		t.Fatalf("oversized request error = %v", err)
	}
	if _, err := os.Stat(started); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized request started evaluator: %v", err)
	}
}

func TestNewRejectsAmbiguousEnvironment(t *testing.T) {
	executable := writeEvaluator(t, "#!/bin/sh\nexit 0\n")
	_, err := evaluatorexec.New(evaluatorexec.Config{
		Executable:  executable,
		Environment: []string{"TOKEN=one", "TOKEN=two"},
	})
	if err == nil || !strings.Contains(err.Error(), "repeats TOKEN") {
		t.Fatalf("duplicate environment error = %v", err)
	}
}

func TestEvaluatorStopsOnContextCancellation(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	executable := writeEvaluator(t, "#!/bin/sh\n: > \"$GRD_TEST_EVALUATOR_STARTED\"\nexec /bin/sleep 30\n")
	evaluator, err := evaluatorexec.New(evaluatorexec.Config{
		Executable:  executable,
		Environment: []string{"GRD_TEST_EVALUATOR_STARTED=" + started},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := evaluator.Evaluate(ctx, evaluation.EvaluationRequest{})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("evaluator subprocess did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Evaluate after cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("evaluator subprocess did not stop after cancellation")
	}
}

func writeEvaluator(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "evaluator")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
