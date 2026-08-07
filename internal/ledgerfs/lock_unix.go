//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package ledgerfs

import (
	"os"

	"golang.org/x/sys/unix"
)

func checkJournalLockSupport() error {
	return nil
}

func lockJournal(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockJournal(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
