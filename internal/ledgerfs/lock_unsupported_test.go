//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package ledgerfs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sky-valley/grd/internal/ledgerfs"
)

func TestOpenOnUnsupportedPlatformDoesNotCreateJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	if ledger, err := ledgerfs.Open(path); err == nil {
		_ = ledger.Close()
		t.Fatal("opened filesystem ledger on unsupported platform")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal path after rejected open: %v, want not exist", err)
	}
}
