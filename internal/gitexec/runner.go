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

// Output executes Git with isolated configuration and returns bounded stdout.
func Output(ctx context.Context, gitDir string, outputLimit int, args ...string) (string, error) {
	if strings.TrimSpace(gitDir) == "" {
		return "", errors.New("git directory is required")
	}
	if outputLimit <= 0 {
		return "", errors.New("git output limit must be positive")
	}
	gitArgs := append([]string{"--git-dir", gitDir}, args...)
	command := exec.CommandContext(ctx, "git", gitArgs...)
	command.Env = environment()
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

func environment() []string {
	environment := make([]string, 0, 6)
	if path := strings.TrimSpace(os.Getenv("PATH")); path != "" {
		environment = append(environment, "PATH="+path)
	}
	return append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
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
