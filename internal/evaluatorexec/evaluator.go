// Package evaluatorexec adapts GRD evaluation to a provider-neutral external
// executable using the evaluatorprotocol JSON contract.
package evaluatorexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sky-valley/grd/internal/evaluation"
	"github.com/sky-valley/grd/internal/evaluatorprotocol"
	"github.com/sky-valley/grd/internal/intent"
)

const maxRequestBytes = 4 * 1024 * 1024
const maxOutputBytes = 1024 * 1024
const maxErrorBytes = 4 * 1024

// Config selects the evaluator executable and its complete explicit
// environment. PATH is inherited only when Config does not provide it.
type Config struct {
	Executable  string
	Environment []string
}

// Evaluator invokes one external process per policy evaluation.
type Evaluator struct {
	executable       string
	workingDirectory string
	environment      []string
}

// New resolves and validates an external evaluator configuration.
func New(config Config) (*Evaluator, error) {
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		return nil, errors.New("evaluator executable is required")
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("find evaluator executable: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("resolve evaluator executable: %w", err)
	}
	environment, err := explicitEnvironment(config.Environment)
	if err != nil {
		return nil, err
	}
	return &Evaluator{
		executable:       resolved,
		workingDirectory: filepath.Dir(resolved),
		environment:      environment,
	}, nil
}

// Evaluate sends one JSON request on stdin and accepts exactly one JSON result
// on stdout. Nonzero exit, malformed output, or cancellation has no durable
// effect; the evaluation orchestrator may retry the immutable request.
func (evaluator *Evaluator) Evaluate(ctx context.Context, request evaluation.EvaluationRequest) (evaluation.EvaluationResult, error) {
	payload, err := json.Marshal(evaluatorprotocol.Request{
		Schema:          evaluatorprotocol.RequestSchema,
		Repository:      request.RepoID,
		Version:         string(request.Version.ID),
		GoverningIntent: string(request.GoverningIntent.ID),
		Purpose:         request.Purpose,
		Priorities:      request.Priorities,
		ChangeEvidence:  request.ChangeEvidence,
		EvaluationPolicy: evaluatorprotocol.Policy{
			Name:        request.Policy.Name,
			Instruction: request.Policy.Instruction,
			Assignee:    request.Policy.Assignee,
		},
	})
	if err != nil {
		return evaluation.EvaluationResult{}, fmt.Errorf("encode evaluator request: %w", err)
	}
	if len(payload) > maxRequestBytes {
		return evaluation.EvaluationResult{}, errors.New("evaluator request exceeds size limit")
	}

	command := exec.CommandContext(ctx, evaluator.executable)
	command.Dir = evaluator.workingDirectory
	command.Env = slices.Clone(evaluator.environment)
	command.Stdin = bytes.NewReader(append(payload, '\n'))
	stdout := &boundedBuffer{remaining: maxOutputBytes}
	stderr := &boundedBuffer{remaining: maxErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return evaluation.EvaluationResult{}, contextErr
		}
		detail := strings.TrimSpace(stderr.String())
		if stderr.overflow {
			detail += " [truncated]"
		}
		if detail == "" {
			return evaluation.EvaluationResult{}, fmt.Errorf("run evaluator: %w", err)
		}
		return evaluation.EvaluationResult{}, fmt.Errorf("run evaluator: %w: %s", err, detail)
	}
	if stdout.overflow {
		return evaluation.EvaluationResult{}, errors.New("evaluator result exceeds size limit")
	}

	var result evaluatorprotocol.Result
	decoder := json.NewDecoder(strings.NewReader(stdout.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return evaluation.EvaluationResult{}, fmt.Errorf("decode evaluator result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return evaluation.EvaluationResult{}, errors.New("evaluator returned more than one JSON value")
		}
		return evaluation.EvaluationResult{}, fmt.Errorf("decode evaluator result trailer: %w", err)
	}
	if result.Schema != evaluatorprotocol.ResultSchema {
		return evaluation.EvaluationResult{}, fmt.Errorf("evaluator result schema = %q, want %q", result.Schema, evaluatorprotocol.ResultSchema)
	}
	if strings.TrimSpace(result.Reason) == "" || len(result.Evidence) == 0 ||
		strings.TrimSpace(result.Provenance.Evaluator) == "" || strings.TrimSpace(result.Provenance.ContractRevision) == "" {
		return evaluation.EvaluationResult{}, errors.New("evaluator result requires reason, evidence, and provenance")
	}
	for _, evidence := range result.Evidence {
		if strings.TrimSpace(evidence) == "" {
			return evaluation.EvaluationResult{}, errors.New("evaluator result contains empty evidence")
		}
	}
	return evaluation.EvaluationResult{
		RequiresAction: result.RequiresAction,
		Reason:         result.Reason,
		Evidence:       slices.Clone(result.Evidence),
		Provenance: intent.EvaluatorProvenance{
			Evaluator:        result.Provenance.Evaluator,
			ContractRevision: result.Provenance.ContractRevision,
		},
	}, nil
}

func explicitEnvironment(configured []string) ([]string, error) {
	environment := make([]string, 0, len(configured)+1)
	seen := make(map[string]struct{}, len(configured)+1)
	for _, entry := range configured {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.ContainsRune(entry, '\x00') {
			return nil, errors.New("evaluator environment entries must be NAME=value")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("evaluator environment repeats %s", name)
		}
		seen[name] = struct{}{}
		environment = append(environment, entry)
	}
	if _, configured := seen["PATH"]; !configured {
		if path := strings.TrimSpace(os.Getenv("PATH")); path != "" {
			environment = append(environment, "PATH="+path)
		}
	}
	return environment, nil
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	overflow  bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if len(value) > buffer.remaining {
		value = value[:buffer.remaining]
		buffer.overflow = true
	}
	_, _ = buffer.buffer.Write(value)
	buffer.remaining -= len(value)
	return original, nil
}

func (buffer *boundedBuffer) String() string {
	return buffer.buffer.String()
}
