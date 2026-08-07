package gitengine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/sky-valley/grd/internal/intent"
)

const privateRefRoot = "refs/grd"
const privateRefNamespace = privateRefRoot + "/"
const versionRefNamespace = privateRefNamespace + "versions"

var fullObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

var ErrTrunkAlreadyInitialized = errors.New("trunk is already initialized")

// Repository adapts one Git repository to GRD's content admission and trunk
// projection contracts.
type Repository struct {
	gitDir   string
	trunkRef string
}

var _ intent.ContentAdmission = (*Repository)(nil)
var _ intent.TrunkProjection = (*Repository)(nil)

// Open validates a Git repository and configures its private GRD refs to stay
// hidden from fetch and push clients.
func Open(ctx context.Context, gitDir, trunkRef string) (*Repository, error) {
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return nil, errors.New("git directory is required")
	}
	trunkRef = strings.TrimSpace(trunkRef)
	if !strings.HasPrefix(trunkRef, "refs/heads/") {
		return nil, errors.New("trunk ref must be under refs/heads")
	}
	repository := &Repository{gitDir: gitDir, trunkRef: trunkRef}
	if err := repository.run(ctx, "check-ref-format", trunkRef); err != nil {
		return nil, fmt.Errorf("validate trunk ref: %w", err)
	}
	if err := repository.rejectPrivateRefCollisions(ctx); err != nil {
		return nil, err
	}
	if err := repository.configurePrivateRefs(ctx); err != nil {
		return nil, fmt.Errorf("hide private GRD refs: %w", err)
	}
	if err := repository.rejectPrivateRefOverrides(ctx); err != nil {
		return nil, err
	}
	return repository, nil
}

func (repository *Repository) rejectPrivateRefCollisions(ctx context.Context) error {
	for _, ref := range []string{privateRefRoot, versionRefNamespace} {
		_, found, err := repository.readRef(ctx, ref)
		if err != nil {
			return fmt.Errorf("inspect private GRD ref namespace: %w", err)
		}
		if found {
			return fmt.Errorf("private GRD ref namespace is blocked by existing ref %s", ref)
		}
	}
	return nil
}

func (repository *Repository) configurePrivateRefs(ctx context.Context) error {
	pattern := `^!?\^?` + regexp.QuoteMeta(privateRefRoot) + `($|/)`
	return repository.run(
		ctx,
		"config",
		"--local",
		"--replace-all",
		"transfer.hideRefs",
		privateRefNamespace,
		pattern,
	)
}

func (repository *Repository) rejectPrivateRefOverrides(ctx context.Context) error {
	for _, key := range []string{"transfer.hideRefs", "receive.hideRefs", "uploadpack.hideRefs"} {
		output, err := repository.output(ctx, "config", "--includes", "--get-all", key)
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
				continue
			}
			return fmt.Errorf("read %s: %w", key, err)
		}
		for _, value := range strings.Split(strings.TrimSpace(output), "\n") {
			value = strings.TrimSpace(value)
			if !strings.HasPrefix(value, "!") {
				continue
			}
			prefix := strings.TrimPrefix(strings.TrimPrefix(value, "!"), "^")
			if privateRefPatternOverlaps(prefix) {
				return fmt.Errorf("%s exposes private GRD refs", key)
			}
		}
	}
	return nil
}

func privateRefPatternOverlaps(prefix string) bool {
	return prefix == "" ||
		strings.HasPrefix(privateRefNamespace, prefix) ||
		strings.HasPrefix(prefix, privateRefNamespace)
}

func (repository *Repository) Admit(ctx context.Context, versionID intent.VersionID, content intent.ContentRef) error {
	oid, err := repository.admissibleCommit(ctx, content)
	if err != nil {
		return err
	}

	versionRef := versionRefNamespace + "/" + string(versionID)
	if err := repository.run(ctx, "check-ref-format", versionRef); err != nil {
		return fmt.Errorf("validate Version content ref: %w", err)
	}
	current, found, err := repository.readRef(ctx, versionRef)
	if err != nil {
		return fmt.Errorf("read Version content ref: %w", err)
	}
	if found {
		if current == oid {
			return nil
		}
		return errors.New("Version content ref already contains different content")
	}
	if err := repository.run(ctx, "update-ref", versionRef, oid, ""); err != nil {
		current, found, readErr := repository.readRef(ctx, versionRef)
		if readErr == nil && found && current == oid {
			return nil
		}
		return fmt.Errorf("create Version content ref: %w", err)
	}
	return nil
}

func (repository *Repository) Bootstrap(ctx context.Context, content intent.ContentRef) error {
	oid, err := gitObjectID(content)
	if err != nil {
		return fmt.Errorf("%w: %v", intent.ErrContentNotAdmissible, err)
	}
	current, found, err := repository.readRef(ctx, repository.trunkRef)
	if err != nil {
		return fmt.Errorf("read trunk ref: %w", err)
	}
	if found {
		if current == oid {
			return nil
		}
		return ErrTrunkAlreadyInitialized
	}
	if err := repository.validateCommit(ctx, oid); err != nil {
		return err
	}
	if err := repository.run(ctx, "update-ref", repository.trunkRef, oid, ""); err != nil {
		current, found, readErr := repository.readRef(ctx, repository.trunkRef)
		if readErr == nil && found {
			if current == oid {
				return nil
			}
			return ErrTrunkAlreadyInitialized
		}
		return fmt.Errorf("initialize trunk ref: %w", err)
	}
	return nil
}

func (repository *Repository) admissibleCommit(ctx context.Context, content intent.ContentRef) (string, error) {
	oid, err := gitObjectID(content)
	if err != nil {
		return "", fmt.Errorf("%w: %v", intent.ErrContentNotAdmissible, err)
	}
	if err := repository.validateCommit(ctx, oid); err != nil {
		return "", err
	}
	return oid, nil
}

func (repository *Repository) validateCommit(ctx context.Context, oid string) error {
	objectType, err := repository.output(ctx, "cat-file", "-t", oid)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("%w: proposed git commit is unavailable", intent.ErrContentNotAdmissible)
		}
		return fmt.Errorf("validate proposed git commit: %w", err)
	}
	if strings.TrimSpace(objectType) != "commit" {
		return fmt.Errorf("%w: proposed git object is not a commit", intent.ErrContentNotAdmissible)
	}
	return nil
}

func (repository *Repository) Current(ctx context.Context) (intent.ContentRef, error) {
	current, found, err := repository.readRef(ctx, repository.trunkRef)
	if err != nil {
		return intent.ContentRef{}, fmt.Errorf("read trunk ref: %w", err)
	}
	if !found {
		return intent.ContentRef{}, errors.New("trunk ref not found")
	}
	return intent.ContentRef{Engine: "git", Revision: current}, nil
}

func (repository *Repository) Advance(ctx context.Context, expected, next intent.ContentRef) error {
	expectedOID, err := gitObjectID(expected)
	if err != nil {
		return fmt.Errorf("expected trunk content: %w", err)
	}
	nextOID, err := gitObjectID(next)
	if err != nil {
		return fmt.Errorf("next trunk content: %w", err)
	}
	if err := repository.validateCommit(ctx, nextOID); err != nil {
		return fmt.Errorf("validate next git commit: %w", err)
	}

	current, found, err := repository.readRef(ctx, repository.trunkRef)
	if err != nil {
		return fmt.Errorf("read trunk ref: %w", err)
	}
	if !found || current != expectedOID {
		return intent.ErrIntentAdvanced
	}
	if err := repository.run(ctx, "update-ref", repository.trunkRef, nextOID, expectedOID); err != nil {
		current, found, readErr := repository.readRef(ctx, repository.trunkRef)
		if readErr == nil && (!found || current != expectedOID) {
			return intent.ErrIntentAdvanced
		}
		return fmt.Errorf("advance trunk ref: %w", err)
	}
	return nil
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

func (repository *Repository) readRef(ctx context.Context, ref string) (string, bool, error) {
	output, err := repository.output(ctx, "rev-parse", "--verify", "--quiet", ref)
	if err == nil {
		return strings.TrimSpace(output), true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, err
}
