package gitengine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const maxGitOutputBytes = 64 * 1024
const maxGitErrorBytes = 4 * 1024

func (repository *Repository) run(ctx context.Context, args ...string) error {
	_, err := repository.output(ctx, args...)
	return err
}

func (repository *Repository) output(ctx context.Context, args ...string) (string, error) {
	gitArgs := append([]string{"--git-dir", repository.gitDir}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = gitProcessEnv()
	stdout := &limitedBuffer{limit: maxGitOutputBytes}
	stderr := &limitedBuffer{limit: maxGitErrorBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", contextErr
		}
		message := strings.TrimSpace(stderr.String())
		truncated := stderr.overflow
		if message == "" {
			message = strings.TrimSpace(stdout.String())
			if len(message) > maxGitErrorBytes {
				message = message[:maxGitErrorBytes]
				truncated = true
			}
			truncated = truncated || stdout.overflow
		}
		if truncated {
			message = strings.TrimSpace(message) + " [truncated]"
		}
		if message != "" {
			return "", fmt.Errorf("git command failed: %w: %s", err, message)
		}
		return "", fmt.Errorf("git command failed: %w", err)
	}
	if stdout.overflow {
		return "", fmt.Errorf("git command output exceeds %d bytes", maxGitOutputBytes)
	}
	return stdout.String(), nil
}

func gitProcessEnv() []string {
	env := make([]string, 0, 4)
	if path := strings.TrimSpace(os.Getenv("PATH")); path != "" {
		env = append(env, "PATH="+path)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining < len(value) {
		buffer.overflow = true
		if remaining < 0 {
			remaining = 0
		}
		value = value[:remaining]
	}
	_, _ = buffer.buffer.Write(value)
	return originalLength, nil
}

func (buffer *limitedBuffer) String() string {
	return buffer.buffer.String()
}
