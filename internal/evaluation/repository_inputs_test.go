package evaluation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sky-valley/grd/internal/evaluation"
	"github.com/sky-valley/grd/internal/intent"
)

const examplePurpose = "A small collaboration service whose accepted state should remain operable, accessible, and easy to evolve safely."

const examplePriorities = `# Priorities

## architecture-data-infrastructure
Assignee: principal:architecture
Instruction: Does this change alter architecture, data models, or infrastructure requirements such as databases, environment variables, services, or deployment topology?

## design-user-experience
Assignee: principal:experience
Instruction: Does this change alter the design system or user experience?

## copy-commercial-impact
Assignee: principal:commercial
Instruction: Does this change alter copywriting or create commercial impact?

## prompts-models
Assignee: principal:models
Instruction: Does this change alter prompts, model selection, or how LLMs are used?
`

func TestRepositoryEvaluationInputSourceUsesGoverningIntentForPolicyAndCandidateOnlyForEvidence(t *testing.T) {
	accepted := intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}
	candidate := intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}
	content := &recordingRepositoryContent{
		files: map[string]string{
			"aaaaaaaa:.grd/purpose.md":    examplePurpose,
			"aaaaaaaa:.grd/priorities.md": examplePriorities,
		},
		difference: "M\tinternal/store/postgres.go\n\n@@ -1 +1 @@\n-memory\n+postgres\n",
	}
	source, err := evaluation.NewRepositoryEvaluationInputSource(content)
	if err != nil {
		t.Fatalf("new repository evaluation input source: %v", err)
	}

	got, err := source.Load(context.Background(), "repo_example", intent.EvaluationContext{
		Version:         intent.Version{Content: candidate},
		GoverningIntent: intent.Revision{Content: accepted},
	})
	if err != nil {
		t.Fatalf("load repository evaluation input: %v", err)
	}
	if got.Purpose != examplePurpose || got.Priorities != examplePriorities || got.ChangeEvidence != content.difference {
		t.Fatalf("evaluation input = %#v", got)
	}
	wantPolicies := []evaluation.Policy{
		{Name: "architecture-data-infrastructure", Assignee: "principal:architecture", Instruction: "Does this change alter architecture, data models, or infrastructure requirements such as databases, environment variables, services, or deployment topology?"},
		{Name: "design-user-experience", Assignee: "principal:experience", Instruction: "Does this change alter the design system or user experience?"},
		{Name: "copy-commercial-impact", Assignee: "principal:commercial", Instruction: "Does this change alter copywriting or create commercial impact?"},
		{Name: "prompts-models", Assignee: "principal:models", Instruction: "Does this change alter prompts, model selection, or how LLMs are used?"},
	}
	if !reflect.DeepEqual(got.Policies, wantPolicies) {
		t.Fatalf("policies = %#v, want %#v", got.Policies, wantPolicies)
	}
	if !reflect.DeepEqual(content.reads, []contentRead{
		{repoID: "repo_example", content: accepted, path: ".grd/purpose.md"},
		{repoID: "repo_example", content: accepted, path: ".grd/priorities.md"},
	}) {
		t.Fatalf("governing reads = %#v", content.reads)
	}
	if content.base != accepted || content.candidate != candidate {
		t.Fatalf("comparison = %#v to %#v, want %#v to %#v", content.base, content.candidate, accepted, candidate)
	}
}

func TestRepositoryEvaluationInputSourceRejectsMalformedAcceptedPrioritiesBeforeComparing(t *testing.T) {
	content := &recordingRepositoryContent{files: map[string]string{
		"aaaaaaaa:.grd/purpose.md":    examplePurpose,
		"aaaaaaaa:.grd/priorities.md": "## architecture\nAssignee: principal:architecture\n",
	}}
	source, err := evaluation.NewRepositoryEvaluationInputSource(content)
	if err != nil {
		t.Fatalf("new repository evaluation input source: %v", err)
	}
	_, err = source.Load(context.Background(), "repo_example", intent.EvaluationContext{
		Version:         intent.Version{Content: intent.ContentRef{Engine: "git", Revision: "bbbbbbbb"}},
		GoverningIntent: intent.Revision{Content: intent.ContentRef{Engine: "git", Revision: "aaaaaaaa"}},
	})
	if err == nil {
		t.Fatal("load malformed priorities succeeded")
	}
	if content.compares != 0 {
		t.Fatalf("content comparisons = %d, want none before valid governing guidance", content.compares)
	}
}

type contentRead struct {
	repoID  string
	content intent.ContentRef
	path    string
}

type recordingRepositoryContent struct {
	files      map[string]string
	reads      []contentRead
	difference string
	base       intent.ContentRef
	candidate  intent.ContentRef
	compares   int
}

func (content *recordingRepositoryContent) ReadText(_ context.Context, repoID string, ref intent.ContentRef, path string) (string, error) {
	content.reads = append(content.reads, contentRead{repoID: repoID, content: ref, path: path})
	value, found := content.files[ref.Revision+":"+path]
	if !found {
		return "", errors.New("file not found")
	}
	return value, nil
}

func (content *recordingRepositoryContent) Compare(_ context.Context, _ string, base, candidate intent.ContentRef) (string, error) {
	content.compares++
	content.base = base
	content.candidate = candidate
	return content.difference, nil
}
