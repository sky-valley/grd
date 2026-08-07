//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package ledgerfs

import (
	"fmt"
	"os"
	"runtime"
)

func checkJournalLockSupport() error {
	return fmt.Errorf("filesystem ledger locking is unsupported on %s", runtime.GOOS)
}

func lockJournal(*os.File) error {
	return checkJournalLockSupport()
}

func unlockJournal(*os.File) error {
	return nil
}
