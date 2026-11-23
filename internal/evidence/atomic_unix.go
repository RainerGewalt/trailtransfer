//go:build !windows

package evidence

import "os"

func syncDir(d *os.File) error { return d.Sync() }
