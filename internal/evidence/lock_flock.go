//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package evidence

import (
	"fmt"
	"os"
	"syscall"
)

// LockJournal takes an exclusive lock on <journal>.lock for the life of the
// process. Two workers that append to one journal would break its chain and
// overwrite each other's job state, so the second one must not start. The
// kernel releases the lock when the process ends, however it ends.
func LockJournal(journalPath string) (release func(), err error) {
	path := journalPath + ".lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("evidence journal %s is in use by another worker process (lock %s): %w", journalPath, path, err)
	}
	return func() { f.Close() }, nil
}
