//go:build !unix

package evidence

// LockJournal is a no-op where flock is not available: two workers on one
// journal are not detected there.
func LockJournal(string) (release func(), err error) { return func() {}, nil }
