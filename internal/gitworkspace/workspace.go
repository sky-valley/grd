// Package gitworkspace adapts a local Git working tree to GRD's engine-neutral
// control protocol.
package gitworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sky-valley/grd/internal/controlhttp"
	"github.com/sky-valley/grd/internal/gitexec"
)

const WorkspaceSchema = "grd.workspace/v1"

type Relation string

const (
	RelationAccepted             Relation = "accepted"
	RelationProposed             Relation = "proposed"
	RelationUnsubmitted          Relation = "unsubmitted"
	RelationBehind               Relation = "behind"
	RelationDiverged             Relation = "diverged"
	RelationReconciled           Relation = "reconciled"
	RelationAcceptedThenAdvanced Relation = "accepted_then_advanced"
)

type Control interface {
	Intent(ctx context.Context) (controlhttp.IntentFact, error)
	Propose(ctx context.Context, idempotencyKey string, proposal controlhttp.ProposalRequest) (controlhttp.ProposalReceipt, error)
	History(ctx context.Context, cursor string, limit int) (controlhttp.HistoryPage, error)
	Version(ctx context.Context, versionID string) (controlhttp.VersionInspection, error)
	Change(ctx context.Context, changeID string) (controlhttp.ChangeInspection, error)
	RebaseHeldVersion(ctx context.Context, idempotencyKey string, request controlhttp.HeldVersionRebaseRequest) (controlhttp.HeldVersionRebaseReceipt, error)
}

type SubmitRequest struct {
	Workdir        string
	IdempotencyKey string
	Dependencies   []string
}

type StatusFact struct {
	Schema     string                         `json:"schema"`
	Repository string                         `json:"repository"`
	Head       controlhttp.Content            `json:"head"`
	Dirty      bool                           `json:"dirty"`
	Relation   Relation                       `json:"relation"`
	Intent     controlhttp.IntentFact         `json:"intent"`
	Version    *controlhttp.VersionInspection `json:"version,omitempty"`
}

const SyncSchema = "grd.workspace-sync/v1"

type SyncRequest struct {
	Workdir        string
	VersionID      string
	IdempotencyKey string
	Rationale      string
}

type SyncFact struct {
	Schema       string                                `json:"schema"`
	Repository   string                                `json:"repository"`
	Action       string                                `json:"action"`
	PreviousHead controlhttp.Content                   `json:"previousHead"`
	Head         controlhttp.Content                   `json:"head"`
	FromVersion  string                                `json:"fromVersion"`
	ToVersion    string                                `json:"toVersion,omitempty"`
	Intent       controlhttp.IntentFact                `json:"intent"`
	Rebase       *controlhttp.HeldVersionRebaseReceipt `json:"rebase,omitempty"`
}

var fullObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func Submit(ctx context.Context, control Control, request SubmitRequest) (controlhttp.ProposalReceipt, error) {
	if control == nil {
		return controlhttp.ProposalReceipt{}, errors.New("GRD control client is required")
	}
	head, dirty, err := inspect(ctx, request.Workdir)
	if err != nil {
		return controlhttp.ProposalReceipt{}, err
	}
	if dirty {
		return controlhttp.ProposalReceipt{}, errors.New("workspace has uncommitted changes; commit them before submitting")
	}
	accepted, err := control.Intent(ctx)
	if err != nil {
		return controlhttp.ProposalReceipt{}, err
	}
	if accepted.Content.Engine != "git" || !fullObjectID.MatchString(accepted.Content.Revision) {
		return controlhttp.ProposalReceipt{}, errors.New("accepted Intent is not represented by an exact Git commit")
	}
	if accepted.Content.Revision == head {
		return controlhttp.ProposalReceipt{}, errors.New("workspace has no new committed content")
	}
	ancestor, err := gitexec.IsAncestor(ctx, request.Workdir, accepted.Content.Revision, head)
	if err != nil {
		return controlhttp.ProposalReceipt{}, err
	}
	if !ancestor {
		return controlhttp.ProposalReceipt{}, errors.New("workspace is not based on current accepted Intent; sync before submitting")
	}
	key := strings.TrimSpace(request.IdempotencyKey)
	if key == "" {
		key = submitIdempotencyKey(accepted.Repository, accepted.Intent, head, request.Dependencies)
	}
	return control.Propose(ctx, key, controlhttp.ProposalRequest{
		Schema:       controlhttp.ProposalSchema,
		BaseIntent:   accepted.Intent,
		Content:      controlhttp.Content{Engine: "git", Revision: head},
		Dependencies: append([]string(nil), request.Dependencies...),
	})
}

func Status(ctx context.Context, control Control, workdir string) (StatusFact, error) {
	if control == nil {
		return StatusFact{}, errors.New("GRD control client is required")
	}
	head, dirty, err := inspect(ctx, workdir)
	if err != nil {
		return StatusFact{}, err
	}
	accepted, err := control.Intent(ctx)
	if err != nil {
		return StatusFact{}, err
	}
	if accepted.Content.Engine != "git" || !fullObjectID.MatchString(accepted.Content.Revision) {
		return StatusFact{}, errors.New("accepted Intent is not represented by an exact Git commit")
	}
	fact := StatusFact{Schema: WorkspaceSchema, Repository: accepted.Repository, Head: controlhttp.Content{Engine: "git", Revision: head}, Dirty: dirty, Intent: accepted}
	if head == accepted.Content.Revision {
		fact.Relation = RelationAccepted
		return fact, nil
	}
	versionIDs, err := versionsForRevision(ctx, control, head)
	if err != nil {
		return StatusFact{}, err
	}
	if len(versionIDs) > 1 {
		return StatusFact{}, fmt.Errorf("workspace HEAD matches multiple GRD Versions (%s); inspect their Changes explicitly", strings.Join(versionIDs, ", "))
	}
	if len(versionIDs) == 1 {
		inspection, err := control.Version(ctx, versionIDs[0])
		if err != nil {
			return StatusFact{}, err
		}
		fact.Version = &inspection
		change, err := control.Change(ctx, inspection.Version.Change)
		if err != nil {
			return StatusFact{}, err
		}
		if change.LatestVersion.ID != inspection.Version.ID {
			fact.Relation = RelationReconciled
			return fact, nil
		}
		if inspection.Promotion == nil {
			fact.Relation = RelationProposed
			return fact, nil
		}
		fact.Relation = RelationAcceptedThenAdvanced
		return fact, nil
	}
	acceptedAncestor, err := gitexec.IsAncestor(ctx, workdir, accepted.Content.Revision, head)
	if err != nil {
		return StatusFact{}, err
	}
	if acceptedAncestor {
		fact.Relation = RelationUnsubmitted
		return fact, nil
	}
	headAncestor, err := gitexec.IsAncestor(ctx, workdir, head, accepted.Content.Revision)
	if err != nil {
		return StatusFact{}, err
	}
	if headAncestor {
		fact.Relation = RelationBehind
	} else {
		fact.Relation = RelationDiverged
	}
	return fact, nil
}

func Sync(ctx context.Context, control Control, request SyncRequest) (SyncFact, error) {
	if control == nil {
		return SyncFact{}, errors.New("GRD control client is required")
	}
	head, dirty, err := inspect(ctx, request.Workdir)
	if err != nil {
		return SyncFact{}, err
	}
	if dirty {
		return SyncFact{}, errors.New("workspace has uncommitted changes; commit or discard them before syncing")
	}
	current, err := control.Intent(ctx)
	if err != nil {
		return SyncFact{}, err
	}
	if current.Content.Engine != "git" || !fullObjectID.MatchString(current.Content.Revision) {
		return SyncFact{}, errors.New("accepted Intent is not represented by an exact Git commit")
	}
	facts, err := readAllHistory(ctx, control)
	if err != nil {
		return SyncFact{}, err
	}
	version, found, err := historyVersion(facts, request.VersionID, head, request.Workdir, ctx)
	if err != nil {
		return SyncFact{}, err
	}
	if !found {
		return SyncFact{}, errors.New("workspace has no submitted Version to sync")
	}
	change, err := control.Change(ctx, version.Change)
	if err != nil {
		return SyncFact{}, err
	}
	result := SyncFact{Schema: SyncSchema, Repository: current.Repository, PreviousHead: controlhttp.Content{Engine: "git", Revision: head}, Head: controlhttp.Content{Engine: "git", Revision: head}, FromVersion: version.ID, Intent: current}
	if change.LatestVersion.ID != version.ID {
		if heldVersionDescends(facts, version.ID, change.LatestVersion.ID) {
			reconciledHead, err := projectRecordedReplacement(ctx, request.Workdir, head, version.Content.Revision, change.LatestVersion.Content.Revision)
			if err != nil {
				return SyncFact{}, err
			}
			head = reconciledHead
			version = change.LatestVersion
			result.Head.Revision = head
			result.ToVersion = version.ID
		} else {
			return syncAmendedWorkspace(ctx, request.Workdir, head, version, change, current, result)
		}
	}
	if version.Content == current.Content {
		result.Action = "workspace_reconciled_to_accepted"
		return result, nil
	}
	if version.BaseIntent == current.Intent {
		if result.ToVersion != "" {
			result.Action = "held_version_already_rebased"
		} else {
			result.Action = "already_current"
		}
		return result, nil
	}
	base, found := historyIntent(facts, version.BaseIntent)
	if !found || base.Content.Engine != "git" || !fullObjectID.MatchString(base.Content.Revision) {
		return SyncFact{}, errors.New("submitted Version governing Intent is not discoverable in repository history")
	}
	if ancestor, err := gitexec.IsAncestor(ctx, request.Workdir, version.Content.Revision, head); err != nil {
		return SyncFact{}, err
	} else if !ancestor {
		return SyncFact{}, errors.New("workspace no longer descends from the submitted Version")
	}
	currentAncestor, err := gitexec.IsAncestor(ctx, request.Workdir, current.Content.Revision, head)
	if err != nil {
		return SyncFact{}, err
	}
	candidateRevision := head
	var temporary *temporaryWorktree
	if !currentAncestor {
		candidateRevision, temporary, err = constructRebasedContent(ctx, request.Workdir, head, current.Content.Revision, base.Content.Revision)
		if err != nil {
			return SyncFact{}, err
		}
		defer temporary.close()
	}
	rationale := strings.TrimSpace(request.Rationale)
	if rationale == "" {
		rationale = "Replay the held Git change onto current accepted Intent."
	}
	key := strings.TrimSpace(request.IdempotencyKey)
	if key == "" {
		key = reconciliationIdempotencyKey("held", current.Repository, version.ID, current.Intent, head)
	}
	receipt, err := control.RebaseHeldVersion(ctx, key, controlhttp.HeldVersionRebaseRequest{
		Schema:          controlhttp.HeldVersionRebaseSchema,
		ExpectedVersion: version.ID,
		ExpectedIntent:  current.Intent,
		Content:         controlhttp.Content{Engine: "git", Revision: candidateRevision},
		Rationale:       rationale,
	})
	if err != nil {
		return SyncFact{}, err
	}
	if temporary != nil {
		if err := temporary.close(); err != nil {
			return SyncFact{}, fmt.Errorf("remove temporary reconciliation worktree: %w", err)
		}
		temporary = nil
	}
	if candidateRevision != head {
		head, err = projectCleanWorkspace(ctx, request.Workdir, head, candidateRevision)
		if err != nil {
			return SyncFact{}, fmt.Errorf("project recorded replacement Version into workspace: %w", err)
		}
	}
	result.Action = "held_version_rebased"
	result.Head.Revision = head
	result.ToVersion = receipt.Version.ID
	result.Rebase = &receipt
	return result, nil
}

func syncAmendedWorkspace(ctx context.Context, workdir, head string, version controlhttp.VersionFact, change controlhttp.ChangeInspection, current controlhttp.IntentFact, result SyncFact) (SyncFact, error) {
	if change.LatestAmendment == nil || change.LatestPromotion == nil {
		return SyncFact{}, errors.New("submitted Change advanced but its latest Version is not yet accepted")
	}
	if change.LatestAmendment.FromVersion != version.ID {
		return SyncFact{}, errors.New("submitted Change has an unsupported multi-step reconciliation lineage")
	}
	target := change.LatestVersion.Content
	if target.Engine != "git" || !fullObjectID.MatchString(target.Revision) {
		return SyncFact{}, errors.New("accepted Amendment is not represented by an exact Git commit")
	}
	if current.Content.Engine != "git" || !fullObjectID.MatchString(current.Content.Revision) {
		return SyncFact{}, errors.New("current Intent is not represented by an exact Git commit")
	}
	if ancestor, err := gitexec.IsAncestor(ctx, workdir, target.Revision, current.Content.Revision); err != nil {
		return SyncFact{}, err
	} else if !ancestor {
		return SyncFact{}, errors.New("accepted Amendment is not in current Git Intent history")
	}
	if ancestor, err := gitexec.IsAncestor(ctx, workdir, current.Content.Revision, head); err != nil {
		return SyncFact{}, err
	} else if !ancestor {
		if sourceAncestor, err := gitexec.IsAncestor(ctx, workdir, version.Content.Revision, head); err != nil {
			return SyncFact{}, err
		} else if !sourceAncestor {
			return SyncFact{}, errors.New("workspace no longer descends from the submitted Version")
		}
		candidate, temporary, err := constructRebasedContent(ctx, workdir, head, current.Content.Revision, version.Content.Revision)
		if err != nil {
			return SyncFact{}, err
		}
		if closeErr := temporary.close(); closeErr != nil {
			return SyncFact{}, fmt.Errorf("remove temporary amendment worktree: %w", closeErr)
		}
		head, err = projectCleanWorkspace(ctx, workdir, head, candidate)
		if err != nil {
			return SyncFact{}, fmt.Errorf("project accepted Amendment into workspace: %w", err)
		}
	}
	result.Action = "workspace_rebased_to_current_intent"
	result.Head.Revision = head
	result.ToVersion = change.LatestVersion.ID
	return result, nil
}

func heldVersionDescends(facts []controlhttp.HistoryEntry, from, to string) bool {
	current := from
	for _, fact := range facts {
		if fact.HeldVersionRebase == nil || fact.Version == nil || fact.HeldVersionRebase.FromVersion != current || fact.HeldVersionRebase.ToVersion != fact.Version.ID {
			continue
		}
		current = fact.Version.ID
		if current == to {
			return true
		}
	}
	return false
}

func projectRecordedReplacement(ctx context.Context, workdir, head, source, target string) (string, error) {
	if !fullObjectID.MatchString(source) || !fullObjectID.MatchString(target) {
		return "", errors.New("recorded Git reconciliation does not identify exact commits")
	}
	if ancestor, err := gitexec.IsAncestor(ctx, workdir, target, head); err != nil {
		return "", err
	} else if ancestor {
		return head, nil
	}
	if ancestor, err := gitexec.IsAncestor(ctx, workdir, source, head); err != nil {
		return "", err
	} else if !ancestor {
		return "", errors.New("workspace no longer descends from the reconciled Version")
	}
	candidate, temporary, err := constructRebasedContent(ctx, workdir, head, target, source)
	if err != nil {
		return "", err
	}
	if closeErr := temporary.close(); closeErr != nil {
		return "", fmt.Errorf("remove temporary recorded-reconciliation worktree: %w", closeErr)
	}
	return projectCleanWorkspace(ctx, workdir, head, candidate)
}

type temporaryWorktree struct {
	repository string
	root       string
	path       string
	closed     bool
}

func constructRebasedContent(ctx context.Context, repository, head, onto, upstream string) (string, *temporaryWorktree, error) {
	if !fullObjectID.MatchString(head) || !fullObjectID.MatchString(onto) || !fullObjectID.MatchString(upstream) {
		return "", nil, errors.New("Git reconciliation requires exact commit ids")
	}
	root, err := os.MkdirTemp("", "grd-sync-")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary reconciliation directory: %w", err)
	}
	temporary := &temporaryWorktree{repository: repository, root: root, path: filepath.Join(root, "worktree")}
	if err := gitexec.RunWorktree(ctx, repository, 64*1024, "worktree", "add", "--detach", temporary.path, head); err != nil {
		_ = temporary.close()
		return "", nil, fmt.Errorf("create temporary reconciliation worktree: %w", err)
	}
	if err := gitexec.RunWorktree(ctx, temporary.path, 1024*1024, "rebase", "--committer-date-is-author-date", "--onto", onto, upstream); err != nil {
		cleanupErr := temporary.close()
		return "", nil, errors.Join(fmt.Errorf("construct reconciled Git content: %w", err), cleanupErr)
	}
	revision, err := gitexec.OutputWorktree(ctx, temporary.path, 256, "rev-parse", "--verify", "HEAD")
	if err != nil {
		cleanupErr := temporary.close()
		return "", nil, errors.Join(fmt.Errorf("read reconciled Git content: %w", err), cleanupErr)
	}
	revision = strings.TrimSpace(revision)
	if !fullObjectID.MatchString(revision) {
		cleanupErr := temporary.close()
		return "", nil, errors.Join(errors.New("reconciled Git content is not an exact commit"), cleanupErr)
	}
	return revision, temporary, nil
}

func (temporary *temporaryWorktree) close() error {
	if temporary == nil || temporary.closed {
		return nil
	}
	temporary.closed = true
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	removeWorktreeErr := gitexec.RunWorktree(cleanupCtx, temporary.repository, 64*1024, "worktree", "remove", "--force", temporary.path)
	removeDirectoryErr := os.RemoveAll(temporary.root)
	return errors.Join(removeWorktreeErr, removeDirectoryErr)
}

func projectCleanWorkspace(ctx context.Context, workdir, expectedHead, target string) (string, error) {
	head, dirty, err := inspect(ctx, workdir)
	if err != nil {
		return "", err
	}
	if dirty || head != expectedHead {
		return "", errors.New("workspace changed while reconciliation was being recorded; rerun sync")
	}
	if head != target {
		if err := gitexec.RunWorktree(ctx, workdir, 1024*1024, "reset", "--keep", target); err != nil {
			return "", err
		}
	}
	head, dirty, err = inspect(ctx, workdir)
	if err != nil {
		return "", err
	}
	if dirty || head != target {
		return "", errors.New("workspace projection did not reach the recorded Git commit")
	}
	return head, nil
}

func readAllHistory(ctx context.Context, control Control) ([]controlhttp.HistoryEntry, error) {
	cursor := ""
	var facts []controlhttp.HistoryEntry
	for {
		page, err := control.History(ctx, cursor, 1)
		if err != nil {
			return nil, err
		}
		facts = append(facts, page.Facts...)
		if page.NextCursor == "" {
			return facts, nil
		}
		if page.NextCursor == cursor {
			return nil, errors.New("history continuation cursor did not advance")
		}
		cursor = page.NextCursor
	}
}

func historyVersion(facts []controlhttp.HistoryEntry, requested, head, workdir string, ctx context.Context) (controlhttp.VersionFact, bool, error) {
	versions := make(map[string]controlhttp.VersionFact)
	latestByChange := make(map[string]string)
	promoted := make(map[string]struct{})
	for _, fact := range facts {
		if fact.Promotion != nil {
			promoted[fact.Promotion.Version] = struct{}{}
		}
		if fact.Version == nil {
			continue
		}
		if err := validateGitVersion(*fact.Version); err != nil {
			return controlhttp.VersionFact{}, false, err
		}
		versions[fact.Version.ID] = *fact.Version
		latestByChange[fact.Version.Change] = fact.Version.ID
	}
	if requested != "" {
		found, ok := versions[requested]
		return found, ok, nil
	}
	candidates := make([]controlhttp.VersionFact, 0, 1)
	for _, found := range versions {
		if latestByChange[found.Change] != found.ID {
			continue
		}
		if _, accepted := promoted[found.ID]; accepted {
			continue
		}
		ancestor, err := gitexec.IsAncestor(ctx, workdir, found.Content.Revision, head)
		if err != nil {
			return controlhttp.VersionFact{}, false, err
		}
		if ancestor {
			candidates = append(candidates, found)
		}
	}
	if len(candidates) > 1 {
		ids := make([]string, len(candidates))
		for index, candidate := range candidates {
			ids[index] = candidate.ID
		}
		slices.Sort(ids)
		return controlhttp.VersionFact{}, false, fmt.Errorf("workspace descends from multiple pending GRD Versions (%s); pass --version", strings.Join(ids, ", "))
	}
	if len(candidates) == 0 {
		return controlhttp.VersionFact{}, false, nil
	}
	return candidates[0], true, nil
}

func historyIntent(facts []controlhttp.HistoryEntry, id string) (controlhttp.HistoryIntentFact, bool) {
	for _, fact := range facts {
		if fact.Intent != nil && fact.Intent.ID == id {
			return *fact.Intent, true
		}
	}
	return controlhttp.HistoryIntentFact{}, false
}

func reconciliationIdempotencyKey(operation, repository, from, to, revision string) string {
	digest := sha256.Sum256([]byte(operation + "\x00" + repository + "\x00" + from + "\x00" + to + "\x00" + revision))
	return "grd-reconcile-" + operation + "-" + hex.EncodeToString(digest[:])
}

func versionsForRevision(ctx context.Context, control Control, revision string) ([]string, error) {
	cursor := ""
	found := make(map[string]struct{})
	for {
		page, err := control.History(ctx, cursor, 1)
		if err != nil {
			return nil, err
		}
		for _, fact := range page.Facts {
			if fact.Version == nil {
				continue
			}
			if err := validateGitVersion(*fact.Version); err != nil {
				return nil, err
			}
			if fact.Version.Content.Revision == revision {
				found[fact.Version.ID] = struct{}{}
			}
		}
		if page.NextCursor == "" {
			ids := make([]string, 0, len(found))
			for id := range found {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			return ids, nil
		}
		if page.NextCursor == cursor {
			return nil, errors.New("history continuation cursor did not advance")
		}
		cursor = page.NextCursor
	}
}

func validateGitVersion(version controlhttp.VersionFact) error {
	if version.Content.Engine != "git" || !fullObjectID.MatchString(version.Content.Revision) {
		return fmt.Errorf("GRD Version %q is not represented by an exact Git commit", version.ID)
	}
	return nil
}

func inspect(ctx context.Context, workdir string) (string, bool, error) {
	head, err := gitexec.OutputWorktree(ctx, workdir, 256, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", false, fmt.Errorf("read workspace HEAD: %w", err)
	}
	head = strings.TrimSpace(head)
	if !fullObjectID.MatchString(head) {
		return "", false, errors.New("workspace HEAD is not an exact Git commit")
	}
	status, err := gitexec.OutputWorktree(ctx, workdir, 1024*1024, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return "", false, fmt.Errorf("read workspace status: %w", err)
	}
	return head, status != "", nil
}

func submitIdempotencyKey(repository, intentID, revision string, dependencies []string) string {
	input := repository + "\x00" + intentID + "\x00" + revision + "\x00" + strings.Join(dependencies, "\x00")
	digest := sha256.Sum256([]byte(input))
	return "grd-submit-" + hex.EncodeToString(digest[:])
}
