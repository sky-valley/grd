//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sky-valley/grd/internal/controlhttp"
	"github.com/sky-valley/grd/internal/evaluatorexec"
)

func TestRunEmitsReadinessReceiptAndReleasesLedger(t *testing.T) {
	gitDir, acceptedOID, _ := stageRuntimeRepository(t)
	ledgerPath := filepath.Join(t.TempDir(), "state", "decision-loop.jsonl")
	evaluator := writeClearEvaluator(t)
	ctx, cancel := context.WithCancel(context.Background())
	stdout := &cancelAfterLineWriter{cancel: cancel}
	var stderr bytes.Buffer

	err := run(ctx, []string{
		"--repository", "repo_example",
		"--git-dir", gitDir,
		"--ledger", ledgerPath,
		"--trunk", "refs/heads/main",
		"--evaluator", evaluator,
		"--poll-interval", "5ms",
	}, stdout, &stderr, os.LookupEnv)
	if err != nil {
		t.Fatalf("run grds: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	ready := decodeHostReady(t, stdout.Bytes())
	if ready.Schema != hostReadySchema || ready.Repository != "repo_example" {
		t.Fatalf("ready receipt = %#v", ready)
	}
	if ready.Content.Engine != "git" || ready.Content.Revision != acceptedOID || ready.Intent == "" {
		t.Fatalf("ready state = %#v", ready)
	}

	reopened, err := openSingleRepository(context.Background(), singleRepositoryConfig{
		RepositoryID: "repo_example",
		GitDir:       gitDir,
		LedgerPath:   ledgerPath,
		TrunkRef:     "refs/heads/main",
		Evaluator:    evaluatorexec.Config{Executable: evaluator},
	})
	if err != nil {
		t.Fatalf("reopen after command shutdown: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened runtime: %v", err)
	}
}

func TestRunServesAcceptedIntentAtReadyControlURL(t *testing.T) {
	gitDir, acceptedOID, _ := stageRuntimeRepository(t)
	ledgerPath := filepath.Join(t.TempDir(), "state", "decision-loop.jsonl")
	evaluator := writeClearEvaluator(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdoutReader, stdoutWriter := io.Pipe()
	defer stdoutReader.Close()
	var stderr bytes.Buffer
	runErr := make(chan error, 1)
	go func() {
		runErr <- run(ctx, []string{
			"--repository", "repo_example",
			"--git-dir", gitDir,
			"--ledger", ledgerPath,
			"--trunk", "refs/heads/main",
			"--evaluator", evaluator,
			"--listen", "127.0.0.1:0",
			"--poll-interval", "5ms",
		}, stdoutWriter, &stderr, os.LookupEnv)
		_ = stdoutWriter.Close()
	}()

	var ready hostReady
	if err := json.NewDecoder(stdoutReader).Decode(&ready); err != nil {
		t.Fatalf("decode host readiness: %v", err)
	}
	if ready.Control == "" {
		t.Fatal("readiness receipt has no control URL")
	}
	response, err := http.Get(ready.Control + "/v1/intent")
	if err != nil {
		t.Fatalf("read served Intent: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Intent status = %d, want 200", response.StatusCode)
	}
	var fact controlhttp.IntentFact
	if err := json.NewDecoder(response.Body).Decode(&fact); err != nil {
		t.Fatalf("decode served Intent: %v", err)
	}
	if fact.Schema != controlhttp.IntentSchema || fact.Repository != "repo_example" || fact.Intent != ready.Intent {
		t.Fatalf("served Intent = %#v, ready = %#v", fact, ready)
	}
	if fact.Content.Engine != "git" || fact.Content.Revision != acceptedOID {
		t.Fatalf("served content = %#v, want git:%s", fact.Content, acceptedOID)
	}

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("run grds: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestParseCommandConfigForwardsOnlyNamedEvaluatorEnvironment(t *testing.T) {
	t.Setenv("GRD_TEST_TOKEN", "secret")
	config, err := parseCommandConfig([]string{
		"--repository", "repo_example",
		"--git-dir", "/tmp/example.git",
		"--ledger", "/tmp/example.jsonl",
		"--evaluator", "/tmp/evaluator",
		"--evaluator-env", "GRD_TEST_TOKEN",
	}, io.Discard, os.LookupEnv)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if len(config.Evaluator.Environment) != 1 || config.Evaluator.Environment[0] != "GRD_TEST_TOKEN=secret" {
		t.Fatalf("evaluator environment = %#v", config.Evaluator.Environment)
	}
	if _, err := parseCommandConfig([]string{
		"--repository", "repo_example",
		"--git-dir", "/tmp/example.git",
		"--ledger", "/tmp/example.jsonl",
		"--evaluator", "/tmp/evaluator",
		"--evaluator-env", "GRD_MISSING_TOKEN",
	}, io.Discard, os.LookupEnv); err == nil {
		t.Fatal("missing named evaluator environment was accepted")
	}
}

func TestRunHelpSucceedsWithoutOpeningRuntime(t *testing.T) {
	var diagnostics bytes.Buffer
	if err := run(context.Background(), []string{"--help"}, io.Discard, &diagnostics, os.LookupEnv); err != nil {
		t.Fatalf("run help: %v", err)
	}
	for _, flagName := range []string{"-repository", "-git-dir", "-ledger", "-evaluator"} {
		if !bytes.Contains(diagnostics.Bytes(), []byte(flagName)) {
			t.Fatalf("help is missing %s:\n%s", flagName, diagnostics.String())
		}
	}
}

func TestParseCommandConfigRejectsInconsistentRuntimeBounds(t *testing.T) {
	base := []string{
		"--repository", "repo_example",
		"--git-dir", "/tmp/example.git",
		"--ledger", "/tmp/example.jsonl",
		"--evaluator", "/tmp/evaluator",
	}
	if _, err := parseCommandConfig(append(base, "--workers", "101"), io.Discard, os.LookupEnv); err == nil {
		t.Fatal("worker count above the pending page limit was accepted")
	}
	spacedGitDir := append([]string(nil), base...)
	spacedGitDir[3] = " /tmp/example.git "
	if _, err := parseCommandConfig(spacedGitDir, io.Discard, os.LookupEnv); err == nil {
		t.Fatal("non-canonical Git directory was accepted")
	}
	if _, err := parseCommandConfig(append(base, "--listen", "0.0.0.0:8787"), io.Discard, os.LookupEnv); err == nil {
		t.Fatal("non-loopback control listener was accepted")
	}
}

type cancelAfterLineWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func decodeHostReady(t *testing.T, value []byte) hostReady {
	t.Helper()
	var ready hostReady
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ready); err != nil {
		t.Fatalf("decode host readiness: %v\n%s", err, value)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("host output contains trailing data: %v\n%s", err, value)
	}
	return ready
}

func (writer *cancelAfterLineWriter) Write(value []byte) (int, error) {
	written, err := writer.Buffer.Write(value)
	if bytes.Contains(value, []byte{'\n'}) {
		writer.cancel()
	}
	return written, err
}
