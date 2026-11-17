//go:build windows

package evidence

import "os"

// Windows cannot fsync a directory handle opened this way; NTFS journals
// the rename itself.
func syncDir(*os.File) error { return nil }
