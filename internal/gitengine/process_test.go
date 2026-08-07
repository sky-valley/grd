//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitengine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sky-valley/grd/internal/gitengine"
)

func TestGitSubprocessDiagnosticsAreBounded(t *testing.T) {
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nprintf '%*s' 100000 x >&2\nexit 2\n"), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir)
	_, err := gitengine.Open(context.Background(), t.TempDir(), "refs/heads/main")
	if err == nil {
		t.Fatal("fake git failure succeeded")
	}
	if len(err.Error()) > 5*1024 {
		t.Fatalf("Git diagnostic length = %d, want bounded error", len(err.Error()))
	}
}

func TestGitSubprocessStopsOnContextCancellation(t *testing.T) {
	binDir := t.TempDir()
	started := filepath.Join(binDir, "started")
	gitPath := filepath.Join(binDir, "git")
	script := fmt.Sprintf("#!/bin/sh\n: > %q\nexec /bin/sleep 30\n", started)
	if err := os.WriteFile(gitPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gitDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := gitengine.Open(ctx, gitDir, "refs/heads/main")
		done <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake git subprocess did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Open after cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Git subprocess did not stop after context cancellation")
	}
}
