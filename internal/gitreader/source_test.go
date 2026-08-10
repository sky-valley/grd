package gitreader_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/grd/internal/evaluation"
	"github.com/sky-valley/grd/internal/gitreader"
	"github.com/sky-valley/grd/internal/intent"
)

var _ evaluation.RepositoryContent = (*gitreader.Source)(nil)

func TestSourceReadsAcceptedGuidanceAndComparesCandidateWithRealGit(t *testing.T) {
	gitDir, accepted, candidate := stageRepository(t)
	source, err := gitreader.NewSource(staticLocator{gitDir: gitDir})
	if err != nil {
		t.Fatalf("new Git reader: %v", err)
	}
	acceptedRef := intent.ContentRef{Engine: "git", Revision: accepted}
	candidateRef := intent.ContentRef{Engine: "git", Revision: candidate}

	purpose, err := source.ReadText(context.Background(), "repo_example", acceptedRef, ".grd/purpose.md")
	if err != nil {
		t.Fatalf("read purpose: %v", err)
	}
	if purpose != "small collaboration service\n" {
		t.Fatalf("purpose = %q", purpose)
	}
	evidence, err := source.Compare(context.Background(), "repo_example", acceptedRef, candidateRef)
	if err != nil {
		t.Fatalf("compare candidate: %v", err)
	}
	for _, want := range []string{
		"M\tinternal/store.ts",
		"diff --git a/internal/store.ts b/internal/store.ts",
		"+export const database = process.env.DATABASE_URL",
	} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("comparison missing %q:\n%s", want, evidence)
		}
	}
}

func TestSourceRejectsNonCommitContentAndUnsafePaths(t *testing.T) {
	gitDir, accepted, candidate := stageRepository(t)
	runGit(t, "--git-dir", gitDir, "tag", "-a", "candidate-tag", candidate, "-m", "annotated candidate")
	tagOID := gitOutput(t, "--git-dir", gitDir, "rev-parse", "refs/tags/candidate-tag")
	source, err := gitreader.NewSource(staticLocator{gitDir: gitDir})
	if err != nil {
		t.Fatalf("new Git reader: %v", err)
	}

	if _, err := source.ReadText(context.Background(), "repo_example", intent.ContentRef{Engine: "git", Revision: tagOID}, ".grd/purpose.md"); err == nil {
		t.Fatal("read through annotated tag object succeeded")
	}
	if _, err := source.Compare(
		context.Background(),
		"repo_example",
		intent.ContentRef{Engine: "git", Revision: accepted},
		intent.ContentRef{Engine: "git", Revision: tagOID},
	); err == nil {
		t.Fatal("compare through annotated tag object succeeded")
	}
	if _, err := source.ReadText(context.Background(), "repo_example", intent.ContentRef{Engine: "jj", Revision: accepted}, ".grd/purpose.md"); err == nil {
		t.Fatal("read non-Git content succeeded")
	}
	if _, err := source.ReadText(context.Background(), "repo_example", intent.ContentRef{Engine: "git", Revision: accepted}, "../purpose.md"); err == nil {
		t.Fatal("read traversal-shaped path succeeded")
	}
}

func TestSourceBoundsRepositoryGuidance(t *testing.T) {
	gitDir, _, _ := stageRepository(t)
	repoDir := filepath.Dir(gitDir)
	writeFile(t, filepath.Join(repoDir, ".grd", "purpose.md"), strings.Repeat("x", 300*1024))
	runGit(t, "-C", repoDir, "add", ".grd/purpose.md")
	runGit(t, "-C", repoDir, "commit", "-m", "oversized guidance")
	oversized := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	source, err := gitreader.NewSource(staticLocator{gitDir: gitDir})
	if err != nil {
		t.Fatalf("new Git reader: %v", err)
	}

	_, err = source.ReadText(context.Background(), "repo_example", intent.ContentRef{Engine: "git", Revision: oversized}, ".grd/purpose.md")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized guidance error = %v", err)
	}
}

func TestSourceRejectsNonUTF8RepositoryText(t *testing.T) {
	gitDir, _, _ := stageRepository(t)
	repoDir := filepath.Dir(gitDir)
	purposePath := filepath.Join(repoDir, ".grd", "purpose.md")
	if err := os.WriteFile(purposePath, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatalf("write invalid UTF-8 guidance: %v", err)
	}
	runGit(t, "-C", repoDir, "add", ".grd/purpose.md")
	runGit(t, "-C", repoDir, "commit", "-m", "invalid guidance encoding")
	invalid := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	source, err := gitreader.NewSource(staticLocator{gitDir: gitDir})
	if err != nil {
		t.Fatalf("new Git reader: %v", err)
	}

	_, err = source.ReadText(context.Background(), "repo_example", intent.ContentRef{Engine: "git", Revision: invalid}, ".grd/purpose.md")
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 guidance error = %v", err)
	}
}

func TestSourceComparisonIgnoresMutableWorktreeAttributes(t *testing.T) {
	gitDir, accepted, candidate := stageRepository(t)
	repoDir := filepath.Dir(gitDir)
	writeFile(t, filepath.Join(repoDir, ".gitattributes"), "internal/store.ts binary\n")
	runGit(t, "--git-dir", gitDir, "config", "diff.noprefix", "true")
	runGit(t, "--git-dir", gitDir, "config", "diff.algorithm", "minimal")
	source, err := gitreader.NewSource(staticLocator{gitDir: gitDir})
	if err != nil {
		t.Fatalf("new Git reader: %v", err)
	}

	evidence, err := source.Compare(
		context.Background(),
		"repo_example",
		intent.ContentRef{Engine: "git", Revision: accepted},
		intent.ContentRef{Engine: "git", Revision: candidate},
	)
	if err != nil {
		t.Fatalf("compare with mutable worktree attributes: %v", err)
	}
	if !strings.Contains(evidence, "+export const database = process.env.DATABASE_URL") {
		t.Fatalf("mutable worktree attributes changed comparison evidence:\n%s", evidence)
	}
	if !strings.Contains(evidence, "diff --git a/internal/store.ts b/internal/store.ts") {
		t.Fatalf("mutable Git config changed comparison paths:\n%s", evidence)
	}
}

func TestSourceRejectsNonUTF8ComparisonEvidence(t *testing.T) {
	gitDir, accepted, _ := stageRepository(t)
	repoDir := filepath.Dir(gitDir)
	changedPath := filepath.Join(repoDir, "internal", "store.ts")
	if err := os.WriteFile(changedPath, []byte{'v', 'a', 'l', 'u', 'e', ':', ' ', 0xff, '\n'}, 0o600); err != nil {
		t.Fatalf("write invalid UTF-8 candidate: %v", err)
	}
	runGit(t, "-C", repoDir, "add", "internal/store.ts")
	runGit(t, "-C", repoDir, "commit", "-m", "invalid candidate encoding")
	candidate := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	source, err := gitreader.NewSource(staticLocator{gitDir: gitDir})
	if err != nil {
		t.Fatalf("new Git reader: %v", err)
	}

	_, err = source.Compare(
		context.Background(),
		"repo_example",
		intent.ContentRef{Engine: "git", Revision: accepted},
		intent.ContentRef{Engine: "git", Revision: candidate},
	)
	if err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 comparison error = %v", err)
	}
}

type staticLocator struct {
	gitDir string
}

func (locator staticLocator) GitDir(context.Context, string) (string, error) {
	return locator.gitDir, nil
}

func stageRepository(t *testing.T) (string, string, string) {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runGit(t, "init", "-b", "main", repoDir)
	runGit(t, "-C", repoDir, "config", "user.name", "GRD Test")
	runGit(t, "-C", repoDir, "config", "user.email", "grd@example.invalid")
	writeFile(t, filepath.Join(repoDir, ".grd", "purpose.md"), "small collaboration service\n")
	writeFile(t, filepath.Join(repoDir, ".grd", "priorities.md"), "# priorities\n")
	writeFile(t, filepath.Join(repoDir, "internal", "store.ts"), "export const store = 'memory'\n")
	runGit(t, "-C", repoDir, "add", ".")
	runGit(t, "-C", repoDir, "commit", "-m", "accepted")
	accepted := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(repoDir, "internal", "store.ts"), "export const database = process.env.DATABASE_URL\n")
	runGit(t, "-C", repoDir, "add", ".")
	runGit(t, "-C", repoDir, "commit", "-m", "candidate")
	candidate := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	return filepath.Join(repoDir, ".git"), accepted, candidate
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	_ = gitOutput(t, args...)
}

func gitOutput(t *testing.T, args ...string) string {
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
