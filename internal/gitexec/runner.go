// Package gitexec runs bounded, isolated Git subprocesses for GRD adapters.
package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const maxErrorBytes = 4 * 1024

// Run executes Git with bounded output and discards successful stdout.
func Run(ctx context.Context, gitDir string, outputLimit int, args ...string) error {
	_, err := Output(ctx, gitDir, outputLimit, args...)
	return err
}

// RunWorktree executes Git against a working tree and discards successful stdout.
func RunWorktree(ctx context.Context, workdir string, outputLimit int, args ...string) error {
	_, err := OutputWorktree(ctx, workdir, outputLimit, args...)
	return err
}

// OutputWorktree executes Git against a working tree with bounded stdout.
func OutputWorktree(ctx context.Context, workdir string, outputLimit int, args ...string) (string, error) {
	if strings.TrimSpace(workdir) == "" {
		return "", errors.New("Git working directory is required")
	}
	gitArgs := append([]string{"-C", workdir, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "rebase.updateRefs=false", "-c", "rerere.enabled=false"}, args...)
	return outputWithEnvironment(ctx, gitArgs, outputLimit, worktreeEnvironment())
}

// IsAncestor reports whether ancestor is in descendant's Git history.
func IsAncestor(ctx context.Context, workdir, ancestor, descendant string) (bool, error) {
	if strings.TrimSpace(workdir) == "" || strings.TrimSpace(ancestor) == "" || strings.TrimSpace(descendant) == "" {
		return false, errors.New("Git ancestry requires working directory and two revisions")
	}
	command := exec.CommandContext(ctx, "git", "-C", workdir, "-c", "core.hooksPath=/dev/null", "-c", "rebase.updateRefs=false", "-c", "rerere.enabled=false", "merge-base", "--is-ancestor", ancestor, descendant)
	command.Env = worktreeEnvironment()
	stderr := &limitedBuffer{limit: maxErrorBytes}
	command.Stdout = &limitedBuffer{limit: maxErrorBytes}
	command.Stderr = stderr
	err := command.Run()
	if err == nil {
		return true, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return false, contextErr
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	message := strings.TrimSpace(stderr.String())
	if message != "" {
		return false, fmt.Errorf("check Git ancestry: %w: %s", err, message)
	}
	return false, fmt.Errorf("check Git ancestry: %w", err)
}

// Output executes Git with isolated configuration and returns bounded stdout.
func Output(ctx context.Context, gitDir string, outputLimit int, args ...string) (string, error) {
	if strings.TrimSpace(gitDir) == "" {
		return "", errors.New("git directory is required")
	}
	if outputLimit <= 0 {
		return "", errors.New("git output limit must be positive")
	}
	gitArgs := append([]string{"--git-dir", gitDir}, args...)
	return output(ctx, gitArgs, outputLimit)
}

func output(ctx context.Context, gitArgs []string, outputLimit int) (string, error) {
	return outputWithEnvironment(ctx, gitArgs, outputLimit, environment())
}

func outputWithEnvironment(ctx context.Context, gitArgs []string, outputLimit int, processEnvironment []string) (string, error) {
	if outputLimit <= 0 {
		return "", errors.New("git output limit must be positive")
	}
	command := exec.CommandContext(ctx, "git", gitArgs...)
	command.Env = processEnvironment
	stdout := &limitedBuffer{limit: outputLimit}
	stderr := &limitedBuffer{limit: maxErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", contextErr
		}
		message := strings.TrimSpace(stderr.String())
		truncated := stderr.overflow
		if message == "" {
			message = strings.TrimSpace(stdout.String())
			if len(message) > maxErrorBytes {
				message = message[:maxErrorBytes]
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
		return "", fmt.Errorf("git command output exceeds %d bytes", outputLimit)
	}
	return stdout.String(), nil
}

func worktreeEnvironment() []string {
	blocked := map[string]struct{}{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
		"GIT_COMMON_DIR":                   {},
		"GIT_CONFIG_GLOBAL":                {},
		"GIT_CONFIG_NOSYSTEM":              {},
		"GIT_CONFIG_SYSTEM":                {},
		"GIT_DIR":                          {},
		"GIT_INDEX_FILE":                   {},
		"GIT_NAMESPACE":                    {},
		"GIT_NO_REPLACE_OBJECTS":           {},
		"GIT_OBJECT_DIRECTORY":             {},
		"GIT_WORK_TREE":                    {},
		"GIT_EDITOR":                       {},
		"GIT_SEQUENCE_EDITOR":              {},
		"GIT_TERMINAL_PROMPT":              {},
		"LC_ALL":                           {},
		"LANG":                             {},
	}
	result := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, skip := blocked[name]; !skip {
			result = append(result, entry)
		}
	}
	return append(result,
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
		"LANG=C",
	)
}

func environment() []string {
	environment := make([]string, 0, 6)
	if path := strings.TrimSpace(os.Getenv("PATH")); path != "" {
		environment = append(environment, "PATH="+path)
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"LC_ALL=C",
		"LANG=C",
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
