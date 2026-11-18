package evidence

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path with data so that after a crash or a power
// loss the file holds either the old or the new content: temporary file,
// fsync, rename, fsync of the directory. Every step that fails is an error;
// a caller that goes on after an error cannot rely on the new content.
func WriteFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// SyncDir makes a directory entry durable: a file that was created or
// renamed in dir survives a power loss only after this.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return syncDir(d)
}
