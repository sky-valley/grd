package gitengine_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/grd/internal/gitengine"
	"github.com/sky-valley/grd/internal/intent"
)

func TestRepositoryAdmitsContentAndAdvancesTrunkWithCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	repository, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open git engine adapter: %v", err)
	}
	versionID := intent.VersionID("version_0123456789abcdef")
	initial := intent.ContentRef{Engine: "git", Revision: fixture.initial}
	proposed := intent.ContentRef{Engine: "git", Revision: fixture.proposed}
	if got, err := repository.Current(ctx); err != nil || got != initial {
		t.Fatalf("current trunk = %#v, error %v; want %#v, nil", got, err, initial)
	}

	if err := repository.Admit(ctx, versionID, proposed); err != nil {
		t.Fatalf("admit proposed content: %v", err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "show-ref", "--verify", "--hash", "refs/grd/versions/"+string(versionID)); got != fixture.proposed {
		t.Fatalf("version ref = %q, want %q", got, fixture.proposed)
	}
	if got := gitOutput(t, "ls-remote", fixture.gitDir, "refs/grd/versions/*"); got != "" {
		t.Fatalf("advertised version refs = %q, want none", got)
	}
	if err := repository.Admit(ctx, versionID, proposed); err != nil {
		t.Fatalf("retry same admission: %v", err)
	}
	if err := repository.Admit(ctx, versionID, initial); err == nil {
		t.Fatal("rebind version ref to different content succeeded")
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "show-ref", "--verify", "--hash", "refs/grd/versions/"+string(versionID)); got != fixture.proposed {
		t.Fatalf("version ref after rejected rebind = %q, want %q", got, fixture.proposed)
	}

	if err := repository.Advance(ctx, initial, proposed); err != nil {
		t.Fatalf("advance trunk: %v", err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.proposed {
		t.Fatalf("trunk = %q, want %q", got, fixture.proposed)
	}
	if got, err := repository.Current(ctx); err != nil || got != proposed {
		t.Fatalf("current trunk after advance = %#v, error %v; want %#v, nil", got, err, proposed)
	}

	if err := repository.Advance(ctx, initial, initial); !errors.Is(err, intent.ErrIntentAdvanced) {
		t.Fatalf("stale advance error = %v, want ErrIntentAdvanced", err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.proposed {
		t.Fatalf("trunk after stale advance = %q, want %q", got, fixture.proposed)
	}
}

func TestRepositoryPrivateNamespaceRejectsExternalPush(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	runGit(t, "--git-dir", fixture.gitDir, "config", "--add", "transfer.hideRefs", "refs/grd/")
	runGit(t, "--git-dir", fixture.gitDir, "config", "--add", "transfer.hideRefs", "!refs/grd")
	repository, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open Git engine: %v", err)
	}
	if err := repository.Admit(ctx, "version_private", intent.ContentRef{Engine: "git", Revision: fixture.proposed}); err != nil {
		t.Fatalf("admit private Version content: %v", err)
	}
	if got := gitOutput(t, "ls-remote", fixture.gitDir, "refs/grd/versions/*"); got != "" {
		t.Fatalf("private Version refs advertised after Open = %q, want none", got)
	}

	cmd := exec.Command("git", "-C", fixture.worktree, "push", fixture.gitDir, "HEAD:refs/grd/versions/forged")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("push into private GRD namespace succeeded:\n%s", output)
	}
	verify := exec.Command("git", "--git-dir", fixture.gitDir, "show-ref", "--verify", "refs/grd/versions/forged")
	if err := verify.Run(); err == nil {
		t.Fatal("rejected push created a private GRD ref")
	}
}

func TestRepositoryPrivateNamespaceRejectsExactRootPush(t *testing.T) {
	fixture := newGitFixture(t)
	if _, err := gitengine.Open(context.Background(), fixture.gitDir, "refs/heads/main"); err != nil {
		t.Fatalf("open Git engine: %v", err)
	}

	cmd := exec.Command("git", "-C", fixture.worktree, "push", fixture.gitDir, "HEAD:refs/grd")
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("push to exact private GRD namespace root succeeded:\n%s", output)
	}
	verify := exec.Command("git", "--git-dir", fixture.gitDir, "show-ref", "--verify", "refs/grd")
	if err := verify.Run(); err == nil {
		t.Fatal("rejected push created the exact private GRD namespace root")
	}
}

func TestRepositoryRejectsPrivateNamespaceCollisionsBeforeConfiguring(t *testing.T) {
	for _, blockingRef := range []string{"refs/grd", "refs/grd/versions"} {
		t.Run(blockingRef, func(t *testing.T) {
			fixture := newGitFixture(t)
			runGit(t, "--git-dir", fixture.gitDir, "update-ref", blockingRef, fixture.initial)
			runGit(t, "--git-dir", fixture.gitDir, "config", "--add", "transfer.hideRefs", "refs/existing-private")
			before := gitOutput(t, "--git-dir", fixture.gitDir, "config", "--get-all", "transfer.hideRefs")

			if _, err := gitengine.Open(context.Background(), fixture.gitDir, "refs/heads/main"); err == nil {
				t.Fatalf("opened repository with blocking ref %q", blockingRef)
			}
			after := gitOutput(t, "--git-dir", fixture.gitDir, "config", "--get-all", "transfer.hideRefs")
			if after != before {
				t.Fatalf("config changed after rejecting %q:\nbefore: %s\nafter:  %s", blockingRef, before, after)
			}
		})
	}
}

func TestRepositoryRejectsIncludedPrivateRefOverride(t *testing.T) {
	for _, test := range []struct {
		name    string
		section string
		value   string
	}{
		{"transfer ancestor", "transfer", "!refs"},
		{"transfer root", "transfer", "!refs/grd"},
		{"receive descendant", "receive", "!^refs/grd/versions/special"},
		{"upload pack root", "uploadpack", "!refs/grd/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			includePath := filepath.Join(fixture.gitDir, "unhide.conf")
			content := "[" + test.section + "]\n\thideRefs = " + test.value + "\n"
			if err := os.WriteFile(includePath, []byte(content), 0o600); err != nil {
				t.Fatalf("write included Git config: %v", err)
			}
			runGit(t, "--git-dir", fixture.gitDir, "config", "--add", "include.path", "unhide.conf")
			if _, err := gitengine.Open(context.Background(), fixture.gitDir, "refs/heads/main"); err == nil {
				t.Fatalf("opened repository with %s = %q", test.section+".hideRefs", test.value)
			}
		})
	}
}

func TestRepositoryAllowsUnrelatedHiddenRefOverride(t *testing.T) {
	fixture := newGitFixture(t)
	runGit(t, "--git-dir", fixture.gitDir, "config", "--add", "transfer.hideRefs", "refs/archive/refs/grd")
	includePath := filepath.Join(fixture.gitDir, "unhide.conf")
	if err := os.WriteFile(includePath, []byte("[transfer]\n\thideRefs = !refs/grd-other\n"), 0o600); err != nil {
		t.Fatalf("write included Git config: %v", err)
	}
	runGit(t, "--git-dir", fixture.gitDir, "config", "--add", "include.path", "unhide.conf")
	if _, err := gitengine.Open(context.Background(), fixture.gitDir, "refs/heads/main"); err != nil {
		t.Fatalf("open repository with unrelated hidden-ref override: %v", err)
	}
	configured := gitOutput(t, "--git-dir", fixture.gitDir, "config", "--get-all", "transfer.hideRefs")
	if !strings.Contains(configured, "refs/archive/refs/grd") {
		t.Fatalf("unrelated hidden-ref rule was replaced:\n%s", configured)
	}
}

func TestRepositoryRejectsWorktreePrivateRefOverride(t *testing.T) {
	fixture := newGitFixture(t)
	runGit(t, "--git-dir", fixture.gitDir, "config", "extensions.worktreeConfig", "true")
	worktreeConfig := filepath.Join(fixture.gitDir, "config.worktree")
	if err := os.WriteFile(worktreeConfig, []byte("[transfer]\n\thideRefs = !refs/grd\n"), 0o600); err != nil {
		t.Fatalf("write worktree Git config: %v", err)
	}
	if _, err := gitengine.Open(context.Background(), fixture.gitDir, "refs/heads/main"); err == nil {
		t.Fatal("opened repository whose worktree Git config exposes private GRD refs")
	}
}

func TestRepositoryDoesNotPinMissingContent(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	repository, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open git engine adapter: %v", err)
	}
	versionID := intent.VersionID("version_missing")

	err = repository.Admit(ctx, versionID, intent.ContentRef{
		Engine:   "git",
		Revision: strings.Repeat("f", 40),
	})
	if err == nil {
		t.Fatal("admit missing content succeeded")
	}
	if !errors.Is(err, intent.ErrContentNotAdmissible) {
		t.Fatalf("missing content error = %v, want ErrContentNotAdmissible", err)
	}
	cmd := exec.Command("git", "--git-dir", fixture.gitDir, "show-ref", "--verify", "refs/grd/versions/"+string(versionID))
	if err := cmd.Run(); err == nil {
		t.Fatal("missing content created a version ref")
	}
}

func TestRepositoryRejectsContentFromAnotherEngine(t *testing.T) {
	fixture := newGitFixture(t)
	repository, err := gitengine.Open(context.Background(), fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	err = repository.Admit(context.Background(), "version_wrong_engine", intent.ContentRef{Engine: "jj", Revision: fixture.proposed})
	if !errors.Is(err, intent.ErrContentNotAdmissible) {
		t.Fatalf("admit error = %v, want ErrContentNotAdmissible", err)
	}
}

func TestRepositoryRejectsAnnotatedTagObject(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	runGit(t, "-C", fixture.worktree, "tag", "-a", "candidate-tag", fixture.proposed, "-m", "candidate tag")
	tagOID := gitOutput(t, "-C", fixture.worktree, "rev-parse", "candidate-tag")
	repository, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	versionID := intent.VersionID("version_tag")
	err = repository.Admit(ctx, versionID, intent.ContentRef{Engine: "git", Revision: tagOID})
	if !errors.Is(err, intent.ErrContentNotAdmissible) {
		t.Fatalf("admit annotated tag error = %v, want ErrContentNotAdmissible", err)
	}
	cmd := exec.Command("git", "--git-dir", fixture.gitDir, "show-ref", "--verify", "refs/grd/versions/"+string(versionID))
	if err := cmd.Run(); err == nil {
		t.Fatal("annotated tag created a Version content ref")
	}
}

func TestRepositoryBootstrapsTrunkExactlyOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	runGit(t, "--git-dir", fixture.gitDir, "update-ref", "-d", "refs/heads/main")
	repository, err := gitengine.Open(ctx, fixture.gitDir, "refs/heads/main")
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	initial := intent.ContentRef{Engine: "git", Revision: fixture.initial}

	if err := repository.Bootstrap(ctx, initial); err != nil {
		t.Fatalf("bootstrap trunk: %v", err)
	}
	if err := repository.Bootstrap(ctx, initial); err != nil {
		t.Fatalf("retry same bootstrap: %v", err)
	}
	if err := repository.Bootstrap(ctx, intent.ContentRef{Engine: "git", Revision: fixture.proposed}); !errors.Is(err, gitengine.ErrTrunkAlreadyInitialized) {
		t.Fatalf("different bootstrap error = %v, want ErrTrunkAlreadyInitialized", err)
	}
	if err := repository.Bootstrap(ctx, intent.ContentRef{Engine: "git", Revision: strings.Repeat("f", 40)}); !errors.Is(err, gitengine.ErrTrunkAlreadyInitialized) {
		t.Fatalf("unavailable second bootstrap error = %v, want ErrTrunkAlreadyInitialized", err)
	}
	if got := gitOutput(t, "--git-dir", fixture.gitDir, "rev-parse", "refs/heads/main"); got != fixture.initial {
		t.Fatalf("trunk after rejected bootstrap = %q, want %q", got, fixture.initial)
	}
}

type gitFixture struct {
	worktree string
	gitDir   string
	initial  string
	proposed string
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runGit(t, "init", "-b", "main", repoDir)
	runGit(t, "-C", repoDir, "config", "user.name", "GRD Test")
	runGit(t, "-C", repoDir, "config", "user.email", "grd@example.invalid")

	writeFixtureFile(t, filepath.Join(repoDir, "message.txt"), "initial\n")
	runGit(t, "-C", repoDir, "add", "message.txt")
	runGit(t, "-C", repoDir, "commit", "-m", "initial")
	initial := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")

	writeFixtureFile(t, filepath.Join(repoDir, "message.txt"), "proposed\n")
	runGit(t, "-C", repoDir, "add", "message.txt")
	runGit(t, "-C", repoDir, "commit", "-m", "proposed")
	proposed := gitOutput(t, "-C", repoDir, "rev-parse", "HEAD")
	runGit(t, "-C", repoDir, "reset", "--hard", initial)

	return gitFixture{
		worktree: repoDir,
		gitDir:   filepath.Join(repoDir, ".git"),
		initial:  initial,
		proposed: proposed,
	}
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
