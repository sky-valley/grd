package gitengine

import (
	"context"

	"github.com/sky-valley/grd/internal/gitexec"
)

const maxGitOutputBytes = 64 * 1024

func (repository *Repository) run(ctx context.Context, args ...string) error {
	return gitexec.Run(ctx, repository.gitDir, maxGitOutputBytes, args...)
}

func (repository *Repository) output(ctx context.Context, args ...string) (string, error) {
	return gitexec.Output(ctx, repository.gitDir, maxGitOutputBytes, args...)
}
