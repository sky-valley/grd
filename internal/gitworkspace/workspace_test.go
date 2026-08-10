package gitworkspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/grd/internal/controlhttp"
	"github.com/sky-valley/grd/internal/gitworkspace"
)

func TestSubmitDerivesExactGitProposalWithoutHiddenWorkspaceState(t *testing.T) {
	workdir, accepted, head := gitFixture(t)
	control := &fakeControl{intent: controlhttp.IntentFact{
		Schema:     controlhttp.IntentSchema,
		Repository: "repo_example",
		Intent:     "intent_one",
		Content:    controlhttp.Content{Engine: "git", Revision: accepted},
	}}

	first, err := gitworkspace.Submit(context.Background(), control, gitworkspace.SubmitRequest{Workdir: workdir})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	second, err := gitworkspace.Submit(context.Background(), control, gitworkspace.SubmitRequest{Workdir: workdir})
	if err != nil {
		t.Fatalf("retry submit: %v", err)
	}
	if control.proposal.BaseIntent != "intent_one" || control.proposal.Content != (controlhttp.Content{Engine: "git", Revision: head}) {
		t.Fatalf("proposal = %#v", control.proposal)
	}
	if len(control.idempotencyKeys) != 2 || control.idempotencyKeys[0] == "" || control.idempotencyKeys[0] != control.idempotencyKeys[1] {
		t.Fatalf("idempotency keys = %#v", control.idempotencyKeys)
	}
	if first.Version.ID != second.Version.ID {
		t.Fatalf("retry receipts = %#v and %#v", first, second)
	}
}

func TestSubmitRejectsUncommittedWorkspaceContent(t *testing.T) {
	workdir, accepted, _ := gitFixture(t)
	if err := os.WriteFile(filepath.Join(workdir, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	control := &fakeControl{intent: controlhttp.IntentFact{Content: controlhttp.Content{Engine: "git", Revision: accepted}}}

	if _, err := gitworkspace.Submit(context.Background(), control, gitworkspace.SubmitRequest{Workdir: workdir}); err == nil {
		t.Fatal("submitted uncommitted workspace content")
	}
	if len(control.idempotencyKeys) != 0 {
		t.Fatal("dirty workspace reached proposal boundary")
	}
}

func TestStatusReconstructsSubmittedVersionFromRemoteFacts(t *testing.T) {
	workdir, accepted, head := gitFixture(t)
	version := controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: head}, Producer: "local:ion"}
	control := &fakeControl{
		intent:     controlhttp.IntentFact{Schema: controlhttp.IntentSchema, Repository: "repo_example", Intent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: accepted}},
		history:    controlhttp.HistoryPage{Schema: controlhttp.HistorySchema, Repository: "repo_example", Facts: []controlhttp.HistoryEntry{{Cursor: "AAAAAAAAAAE", Kind: "version_proposed", Change: &controlhttp.ChangeFact{ID: "change_one"}, Version: &version}}},
		inspection: controlhttp.VersionInspection{Schema: controlhttp.VersionSchema, Repository: "repo_example", Version: version},
		change:     controlhttp.ChangeInspection{Schema: controlhttp.ChangeSchema, Repository: "repo_example", Change: controlhttp.ChangeFact{ID: "change_one"}, LatestVersion: version},
	}

	status, err := gitworkspace.Status(context.Background(), control, workdir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Relation != gitworkspace.RelationProposed || status.Version == nil || status.Version.Version.ID != "version_one" || status.Head.Revision != head {
		t.Fatalf("workspace status = %#v", status)
	}
}

func TestStatusRefusesAmbiguousVersionsForTheSameCommit(t *testing.T) {
	workdir, accepted, head := gitFixture(t)
	first := controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: head}, Producer: "local:ion"}
	second := controlhttp.VersionFact{ID: "version_two", Change: "change_two", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: head}, Producer: "local:ion"}
	control := &fakeControl{
		intent: controlhttp.IntentFact{Schema: controlhttp.IntentSchema, Repository: "repo_example", Intent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: accepted}},
		history: controlhttp.HistoryPage{Facts: []controlhttp.HistoryEntry{
			{Version: &first},
			{Version: &second},
		}},
	}

	if _, err := gitworkspace.Status(context.Background(), control, workdir); err == nil || !strings.Contains(err.Error(), "multiple GRD Versions") {
		t.Fatalf("ambiguous status error = %v", err)
	}
}

func TestStatusIdentifiesAReconciledVersionInsteadOfCallingItActive(t *testing.T) {
	workdir, accepted, head := gitFixture(t)
	original := controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: head}, Producer: "local:ion"}
	replacement := controlhttp.VersionFact{ID: "version_two", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: accepted}, Producer: "local:ion"}
	control := &fakeControl{
		intent:     controlhttp.IntentFact{Schema: controlhttp.IntentSchema, Repository: "repo_example", Intent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: accepted}},
		history:    controlhttp.HistoryPage{Facts: []controlhttp.HistoryEntry{{Version: &original}, {Version: &replacement}}},
		inspection: controlhttp.VersionInspection{Schema: controlhttp.VersionSchema, Repository: "repo_example", Version: original},
		change:     controlhttp.ChangeInspection{Schema: controlhttp.ChangeSchema, Repository: "repo_example", Change: controlhttp.ChangeFact{ID: "change_one"}, LatestVersion: replacement},
	}

	status, err := gitworkspace.Status(context.Background(), control, workdir)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Relation != gitworkspace.RelationReconciled {
		t.Fatalf("status relation = %q, want reconciled", status.Relation)
	}
}

func TestStatusRejectsAHistoryRevisionThatIsNotAnExactGitObjectID(t *testing.T) {
	workdir, accepted, _ := gitFixture(t)
	malformed := controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: "--option-like-revision"}, Producer: "local:ion"}
	control := &fakeControl{
		intent:  controlhttp.IntentFact{Schema: controlhttp.IntentSchema, Repository: "repo_example", Intent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: accepted}},
		history: controlhttp.HistoryPage{Facts: []controlhttp.HistoryEntry{{Version: &malformed}}},
	}

	if _, err := gitworkspace.Status(context.Background(), control, workdir); err == nil || !strings.Contains(err.Error(), "exact Git commit") {
		t.Fatalf("malformed Git history error = %v", err)
	}
}

func TestSyncRebasesHeldVersionAndRecordsReplacementFact(t *testing.T) {
	workdir, accepted, head := gitFixture(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = Global GRD Test\n\temail = global-grd@example.invalid\n"), 0o600); err != nil {
		t.Fatalf("write global Git identity: %v", err)
	}
	t.Setenv("HOME", home)
	gitRun(t, workdir, "config", "--unset-all", "user.name")
	gitRun(t, workdir, "config", "--unset-all", "user.email")
	gitRun(t, workdir, "switch", "-c", "accepted-update", accepted)
	if err := os.WriteFile(filepath.Join(workdir, "accepted.txt"), []byte("accepted update\n"), 0o600); err != nil {
		t.Fatalf("write accepted update: %v", err)
	}
	gitRun(t, workdir, "add", "accepted.txt")
	gitRun(t, workdir, "commit", "-m", "advance accepted Intent")
	current := gitOutput(t, workdir, "rev-parse", "HEAD")
	gitRun(t, workdir, "switch", "-C", "candidate", head)
	version := controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: head}, Producer: "local:ion"}
	control := &fakeControl{
		intent: controlhttp.IntentFact{Schema: controlhttp.IntentSchema, Repository: "repo_example", Intent: "intent_two", Content: controlhttp.Content{Engine: "git", Revision: current}},
		history: controlhttp.HistoryPage{Schema: controlhttp.HistorySchema, Repository: "repo_example", Facts: []controlhttp.HistoryEntry{
			{Cursor: "AAAAAAAAAAE", Kind: "intent_initialized", Intent: &controlhttp.HistoryIntentFact{ID: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: accepted}}},
			{Cursor: "AAAAAAAAAAI", Kind: "version_proposed", Change: &controlhttp.ChangeFact{ID: "change_one"}, Version: &version},
		}},
		change: controlhttp.ChangeInspection{Schema: controlhttp.ChangeSchema, Repository: "repo_example", Change: controlhttp.ChangeFact{ID: "change_one"}, LatestVersion: version},
	}

	fact, err := gitworkspace.Sync(context.Background(), control, gitworkspace.SyncRequest{Workdir: workdir, VersionID: "version_one", Rationale: "Replay the held change onto current Intent."})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	newHead := gitOutput(t, workdir, "rev-parse", "HEAD")
	if newHead == head || fact.Head.Revision != newHead || control.rebaseRequest.Content.Revision != newHead || control.rebaseRequest.ExpectedIntent != "intent_two" {
		t.Fatalf("sync fact = %#v, rebase request = %#v", fact, control.rebaseRequest)
	}
	command := exec.Command("git", "-C", workdir, "merge-base", "--is-ancestor", current, newHead)
	if err := command.Run(); err != nil {
		t.Fatalf("rebased HEAD does not descend from current Intent: %v", err)
	}
}

func TestSyncRestoresWorkspaceWhenReplacementFactIsNotConfirmed(t *testing.T) {
	workdir, accepted, head := gitFixture(t)
	gitRun(t, workdir, "switch", "-c", "accepted-update", accepted)
	if err := os.WriteFile(filepath.Join(workdir, "accepted.txt"), []byte("accepted update\n"), 0o600); err != nil {
		t.Fatalf("write accepted update: %v", err)
	}
	gitRun(t, workdir, "add", "accepted.txt")
	gitRun(t, workdir, "commit", "-m", "advance accepted Intent")
	current := gitOutput(t, workdir, "rev-parse", "HEAD")
	gitRun(t, workdir, "switch", "-C", "candidate", head)
	version := controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: head}, Producer: "local:ion"}
	control := new(fakeControl)
	control.intent = controlhttp.IntentFact{Schema: controlhttp.IntentSchema, Repository: "repo_example", Intent: "intent_two", Content: controlhttp.Content{Engine: "git", Revision: current}}
	control.history = controlhttp.HistoryPage{
		Schema:     controlhttp.HistorySchema,
		Repository: "repo_example",
		Facts: []controlhttp.HistoryEntry{
			{Cursor: "AAAAAAAAAAE", Kind: "intent_initialized", Intent: &controlhttp.HistoryIntentFact{ID: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: accepted}}},
			{Cursor: "AAAAAAAAAAI", Kind: "version_proposed", Change: &controlhttp.ChangeFact{ID: "change_one"}, Version: &version},
		},
	}
	control.change = controlhttp.ChangeInspection{Schema: controlhttp.ChangeSchema, Repository: "repo_example", Change: controlhttp.ChangeFact{ID: "change_one"}, LatestVersion: version}
	control.rebaseErr = errors.New("control response lost")

	if _, err := gitworkspace.Sync(context.Background(), control, gitworkspace.SyncRequest{Workdir: workdir, VersionID: "version_one"}); err == nil {
		t.Fatal("sync succeeded without a confirmed replacement fact")
	}
	if got := gitOutput(t, workdir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD after failed record = %s, want original %s", got, head)
	}
	if got := gitOutput(t, workdir, "status", "--porcelain=v1"); got != "" {
		t.Fatalf("workspace after failed record is dirty: %q", got)
	}
}

func TestSyncRecognizesAReplacementRecordedBeforeAnInterruptedRetry(t *testing.T) {
	workdir, accepted, head := gitFixture(t)
	gitRun(t, workdir, "switch", "-c", "accepted-update", accepted)
	if err := os.WriteFile(filepath.Join(workdir, "accepted.txt"), []byte("accepted update\n"), 0o600); err != nil {
		t.Fatalf("write accepted update: %v", err)
	}
	gitRun(t, workdir, "add", "accepted.txt")
	gitRun(t, workdir, "commit", "-m", "advance accepted Intent")
	current := gitOutput(t, workdir, "rev-parse", "HEAD")
	gitRun(t, workdir, "switch", "-C", "candidate", head)
	gitRun(t, workdir, "rebase", "--committer-date-is-author-date", "--onto", current, accepted)
	rebased := gitOutput(t, workdir, "rev-parse", "HEAD")
	gitRun(t, workdir, "reset", "--hard", head)
	original := controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: head}, Producer: "local:ion"}
	replacement := controlhttp.VersionFact{ID: "version_two", Change: "change_one", BaseIntent: "intent_two", Content: controlhttp.Content{Engine: "git", Revision: rebased}, Producer: "local:ion"}
	control := new(fakeControl)
	control.intent = controlhttp.IntentFact{Schema: controlhttp.IntentSchema, Repository: "repo_example", Intent: "intent_two", Content: controlhttp.Content{Engine: "git", Revision: current}}
	control.history = controlhttp.HistoryPage{
		Schema:     controlhttp.HistorySchema,
		Repository: "repo_example",
		Facts: []controlhttp.HistoryEntry{
			{Cursor: "AAAAAAAAAAE", Kind: "intent_initialized", Intent: &controlhttp.HistoryIntentFact{ID: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: accepted}}},
			{Cursor: "AAAAAAAAAAI", Kind: "version_proposed", Change: &controlhttp.ChangeFact{ID: "change_one"}, Version: &original},
			{Cursor: "AAAAAAAAAAM", Kind: "held_version_rebased", Version: &replacement, HeldVersionRebase: &controlhttp.HeldVersionRebaseFact{FromVersion: original.ID, ToVersion: replacement.ID, FromIntent: "intent_one", ToIntent: "intent_two", Rationale: "Replay onto current Intent."}},
		},
	}
	control.change = controlhttp.ChangeInspection{Schema: controlhttp.ChangeSchema, Repository: "repo_example", Change: controlhttp.ChangeFact{ID: "change_one"}, LatestVersion: replacement}

	fact, err := gitworkspace.Sync(context.Background(), control, gitworkspace.SyncRequest{Workdir: workdir, VersionID: original.ID})
	if err != nil {
		t.Fatalf("retry sync: %v", err)
	}
	if fact.Action != "held_version_already_rebased" || fact.FromVersion != original.ID || fact.ToVersion != replacement.ID || fact.Head.Revision != rebased {
		t.Fatalf("retry sync fact = %#v", fact)
	}
	if control.rebaseRequest != (controlhttp.HeldVersionRebaseRequest{}) {
		t.Fatalf("retry wrote another replacement fact: %#v", control.rebaseRequest)
	}
}

func TestSyncReplaysLocalSuffixOntoCurrentIntentAfterAnAcceptedAmendment(t *testing.T) {
	workdir, accepted, submittedHead := gitFixture(t)
	gitRun(t, workdir, "switch", "-c", "accepted-amendment", accepted)
	if err := os.WriteFile(filepath.Join(workdir, "README.md"), []byte("accepted amendment\n"), 0o600); err != nil {
		t.Fatalf("write amendment: %v", err)
	}
	gitRun(t, workdir, "add", "README.md")
	gitRun(t, workdir, "commit", "-m", "accept amended change")
	amendedHead := gitOutput(t, workdir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(workdir, "later.txt"), []byte("later accepted change\n"), 0o600); err != nil {
		t.Fatalf("write later change: %v", err)
	}
	gitRun(t, workdir, "add", "later.txt")
	gitRun(t, workdir, "commit", "-m", "advance beyond amendment")
	current := gitOutput(t, workdir, "rev-parse", "HEAD")

	gitRun(t, workdir, "switch", "-C", "candidate", submittedHead)
	if err := os.WriteFile(filepath.Join(workdir, "local.txt"), []byte("continuing local work\n"), 0o600); err != nil {
		t.Fatalf("write local suffix: %v", err)
	}
	gitRun(t, workdir, "add", "local.txt")
	gitRun(t, workdir, "commit", "-m", "continue after submission")
	original := controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: submittedHead}, Producer: "local:ion"}
	replacement := controlhttp.VersionFact{ID: "version_two", Change: "change_one", BaseIntent: "intent_one", Content: controlhttp.Content{Engine: "git", Revision: amendedHead}, Producer: "local:ion"}
	control := &fakeControl{
		intent:  controlhttp.IntentFact{Schema: controlhttp.IntentSchema, Repository: "repo_example", Intent: "intent_three", Content: controlhttp.Content{Engine: "git", Revision: current}},
		history: controlhttp.HistoryPage{Facts: []controlhttp.HistoryEntry{{Version: &original}, {Version: &replacement}}},
		change: controlhttp.ChangeInspection{
			Schema:          controlhttp.ChangeSchema,
			Repository:      "repo_example",
			Change:          controlhttp.ChangeFact{ID: "change_one"},
			LatestVersion:   replacement,
			LatestAmendment: &controlhttp.AmendmentFact{FromVersion: original.ID, ToVersion: replacement.ID, Rationale: "Apply accepted amendment."},
			LatestPromotion: &controlhttp.PromotionFact{ID: "promotion_one", FromIntent: "intent_one", ToIntent: "intent_two", Version: replacement.ID},
		},
	}

	fact, err := gitworkspace.Sync(context.Background(), control, gitworkspace.SyncRequest{Workdir: workdir, VersionID: original.ID})
	if err != nil {
		t.Fatalf("sync accepted Amendment: %v", err)
	}
	newHead := gitOutput(t, workdir, "rev-parse", "HEAD")
	if fact.Action != "workspace_rebased_to_current_intent" || fact.Head.Revision != newHead || fact.ToVersion != replacement.ID {
		t.Fatalf("amendment sync fact = %#v", fact)
	}
	if err := exec.Command("git", "-C", workdir, "merge-base", "--is-ancestor", current, newHead).Run(); err != nil {
		t.Fatalf("synced workspace remains behind current Intent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "later.txt")); err != nil {
		t.Fatalf("later accepted content is absent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workdir, "local.txt")); err != nil {
		t.Fatalf("continuing local work is absent: %v", err)
	}
}

type fakeControl struct {
	intent          controlhttp.IntentFact
	proposal        controlhttp.ProposalRequest
	idempotencyKeys []string
	history         controlhttp.HistoryPage
	inspection      controlhttp.VersionInspection
	change          controlhttp.ChangeInspection
	rebaseRequest   controlhttp.HeldVersionRebaseRequest
	rebaseErr       error
}

func (control *fakeControl) Intent(context.Context) (controlhttp.IntentFact, error) {
	return control.intent, nil
}

func (control *fakeControl) Propose(_ context.Context, key string, proposal controlhttp.ProposalRequest) (controlhttp.ProposalReceipt, error) {
	control.idempotencyKeys = append(control.idempotencyKeys, key)
	control.proposal = proposal
	return controlhttp.ProposalReceipt{
		Schema:     controlhttp.ProposalReceiptSchema,
		Repository: "repo_example",
		Change:     controlhttp.ChangeFact{ID: "change_one"},
		Version:    controlhttp.VersionFact{ID: "version_one", Change: "change_one", BaseIntent: proposal.BaseIntent, Content: proposal.Content, Producer: "local:ion", Dependencies: proposal.Dependencies},
	}, nil
}

func (control *fakeControl) History(context.Context, string, int) (controlhttp.HistoryPage, error) {
	return control.history, nil
}

func (control *fakeControl) Version(context.Context, string) (controlhttp.VersionInspection, error) {
	return control.inspection, nil
}

func (control *fakeControl) Change(context.Context, string) (controlhttp.ChangeInspection, error) {
	return control.change, nil
}

func (control *fakeControl) RebaseHeldVersion(_ context.Context, _ string, request controlhttp.HeldVersionRebaseRequest) (controlhttp.HeldVersionRebaseReceipt, error) {
	control.rebaseRequest = request
	if control.rebaseErr != nil {
		return controlhttp.HeldVersionRebaseReceipt{}, control.rebaseErr
	}
	return controlhttp.HeldVersionRebaseReceipt{
		Schema:     controlhttp.HeldVersionRebaseReceiptSchema,
		Repository: "repo_example",
		Change:     controlhttp.ChangeFact{ID: "change_one"},
		Version:    controlhttp.VersionFact{ID: "version_two", Change: "change_one", BaseIntent: request.ExpectedIntent, Content: request.Content, Producer: "local:ion"},
		Rebase:     controlhttp.HeldVersionRebaseFact{FromVersion: request.ExpectedVersion, ToVersion: "version_two", FromIntent: "intent_one", ToIntent: request.ExpectedIntent, Rationale: request.Rationale},
	}, nil
}

func gitFixture(t *testing.T) (string, string, string) {
	t.Helper()
	workdir := t.TempDir()
	gitRun(t, workdir, "init", "-b", "main")
	gitRun(t, workdir, "config", "user.name", "GRD Test")
	gitRun(t, workdir, "config", "user.email", "grd@example.invalid")
	if err := os.WriteFile(filepath.Join(workdir, "README.md"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatalf("write accepted file: %v", err)
	}
	gitRun(t, workdir, "add", "README.md")
	gitRun(t, workdir, "commit", "-m", "accepted")
	accepted := gitOutput(t, workdir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(workdir, "README.md"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}
	gitRun(t, workdir, "add", "README.md")
	gitRun(t, workdir, "commit", "-m", "candidate")
	return workdir, accepted, gitOutput(t, workdir, "rev-parse", "HEAD")
}

func gitRun(t *testing.T, workdir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", workdir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func gitOutput(t *testing.T, workdir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", workdir}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSuffix(string(output), "\n")
}
