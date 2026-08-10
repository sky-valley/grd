// Package gitreader reads bounded repository guidance and change evidence from
// Git commits for GRD evaluation.
package gitreader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sky-valley/grd/internal/gitexec"
	"github.com/sky-valley/grd/internal/intent"
)

const (
	maxGuidanceBytes    = 256 * 1024
	maxChangedPathBytes = 128 * 1024
	maxPatchBytes       = 1024 * 1024
	maxObjectTypeBytes  = 64
)

var fullObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// RepositoryLocator resolves GRD's opaque repository identity to a Git
// directory without exposing storage or server types to the reader.
type RepositoryLocator interface {
	GitDir(ctx context.Context, repoID string) (string, error)
}

// Source reads immutable repository content from Git commits.
type Source struct {
	locator RepositoryLocator
}

// NewSource constructs a Git-backed repository content source.
func NewSource(locator RepositoryLocator) (*Source, error) {
	if locator == nil {
		return nil, errors.New("Git reader requires a repository locator")
	}
	return &Source{locator: locator}, nil
}

// ReadText returns one UTF-8 repository file from an exact commit.
func (source *Source) ReadText(ctx context.Context, repoID string, content intent.ContentRef, filePath string) (string, error) {
	oid, err := gitObjectID(content)
	if err != nil {
		return "", err
	}
	if !safeTreePath(filePath) {
		return "", errors.New("repository content path must be a clean relative path")
	}
	gitDir, err := source.repositoryPath(ctx, repoID)
	if err != nil {
		return "", err
	}
	if err := validateCommit(ctx, gitDir, oid); err != nil {
		return "", err
	}
	output, err := gitexec.Output(ctx, gitDir, maxGuidanceBytes, "cat-file", "blob", oid+":"+filePath)
	if err != nil {
		return "", fmt.Errorf("read repository content %s: %w", filePath, err)
	}
	if !utf8.ValidString(output) {
		return "", fmt.Errorf("repository content %s must be UTF-8 text", filePath)
	}
	return output, nil
}

// Compare returns bounded changed-path and patch evidence between two commits.
func (source *Source) Compare(ctx context.Context, repoID string, base, candidate intent.ContentRef) (string, error) {
	baseOID, err := gitObjectID(base)
	if err != nil {
		return "", fmt.Errorf("comparison base: %w", err)
	}
	candidateOID, err := gitObjectID(candidate)
	if err != nil {
		return "", fmt.Errorf("comparison candidate: %w", err)
	}
	gitDir, err := source.repositoryPath(ctx, repoID)
	if err != nil {
		return "", err
	}
	if err := validateCommit(ctx, gitDir, baseOID); err != nil {
		return "", fmt.Errorf("comparison base: %w", err)
	}
	if err := validateCommit(ctx, gitDir, candidateOID); err != nil {
		return "", fmt.Errorf("comparison candidate: %w", err)
	}
	diffOptions := []string{
		"-c", "core.quotePath=true",
		"diff",
		"-O" + os.DevNull,
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--find-renames=50%",
		"--diff-algorithm=histogram",
		"--no-indent-heuristic",
		"--unified=3",
		"--inter-hunk-context=0",
		"--full-index",
		"--submodule=short",
		"--src-prefix=a/",
		"--dst-prefix=b/",
		"--no-relative",
	}
	changedArgs := append(slices.Clone(diffOptions), "--name-status", baseOID, candidateOID, "--")
	changed, err := gitexec.Output(ctx, gitDir, maxChangedPathBytes, changedArgs...)
	if err != nil {
		return "", fmt.Errorf("list changed repository paths: %w", err)
	}
	patchArgs := append(slices.Clone(diffOptions), "--patch", baseOID, candidateOID, "--")
	patch, err := gitexec.Output(ctx, gitDir, maxPatchBytes, patchArgs...)
	if err != nil {
		return "", fmt.Errorf("read repository comparison: %w", err)
	}
	if strings.TrimSpace(changed) == "" && strings.TrimSpace(patch) == "" {
		return "", errors.New("repository comparison is empty")
	}
	evidence := "Changed paths:\n" + changed + "\nPatch:\n" + patch
	if !utf8.ValidString(evidence) {
		return "", errors.New("repository comparison must be UTF-8 text")
	}
	return evidence, nil
}

func (source *Source) repositoryPath(ctx context.Context, repoID string) (string, error) {
	if strings.TrimSpace(repoID) == "" {
		return "", errors.New("repository id is required")
	}
	gitDir, err := source.locator.GitDir(ctx, repoID)
	if err != nil {
		return "", fmt.Errorf("locate repository content: %w", err)
	}
	if strings.TrimSpace(gitDir) == "" {
		return "", errors.New("repository content location is empty")
	}
	return gitDir, nil
}

func gitObjectID(content intent.ContentRef) (string, error) {
	if content.Engine != "git" {
		return "", fmt.Errorf("content engine %q is not git", content.Engine)
	}
	if !fullObjectID.MatchString(content.Revision) {
		return "", errors.New("git content revision must be a full lowercase object id")
	}
	return content.Revision, nil
}

func validateCommit(ctx context.Context, gitDir, oid string) error {
	objectType, err := gitexec.Output(ctx, gitDir, maxObjectTypeBytes, "cat-file", "-t", oid)
	if err != nil {
		return fmt.Errorf("validate git commit: %w", err)
	}
	if strings.TrimSpace(objectType) != "commit" {
		return errors.New("git content revision is not a commit")
	}
	return nil
}

func safeTreePath(filePath string) bool {
	return filePath != "" &&
		filePath == strings.TrimSpace(filePath) &&
		!strings.HasPrefix(filePath, "/") &&
		!strings.Contains(filePath, ":") &&
		!strings.ContainsRune(filePath, '\x00') &&
		path.Clean(filePath) == filePath &&
		filePath != "." &&
		!strings.HasPrefix(filePath, "../")
}
