package policy

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxPathLength bounds a source or target value.
const maxPathLength = 4096

// location is a source or target as rclone reads it: a reference to a
// configured remote ("name:path") or an absolute local path.
//
// The same parser reads the policy's allowed_sources and allowed_targets and
// the command's source and target, so both sides of a comparison are
// normalised the same way.
type location struct {
	remote string // remote name before the first ':'; empty for a local path
	path   string // segments joined by '/', a leading '/' kept, no '.' or empty segments
}

// String is the normalised form that is handed to rclone and recorded.
func (l location) String() string {
	if l.remote == "" {
		return l.path
	}
	return l.remote + ":" + l.path
}

func (l location) absolute() bool { return strings.HasPrefix(l.path, "/") }

// parseLocation splits s into remote and path and normalises the path:
// repeated slashes and "." segments are removed, a trailing slash is dropped.
// It refuses everything whose meaning could differ between this comparison
// and rclone or the storage behind it: parent segments in any spelling
// (including percent-encoded and look-alike dots), backslashes, control
// characters, rclone's on-the-fly backends (":backend:") and connection-string
// parameters ("remote,param=value:"), relative local paths, and values longer
// than 4096 bytes.
func parseLocation(s string) (location, error) {
	switch {
	case s == "":
		return location{}, errors.New("is empty")
	case len(s) > maxPathLength:
		return location{}, fmt.Errorf("is longer than %d bytes", maxPathLength)
	case !utf8.ValidString(s):
		return location{}, errors.New("is not valid UTF-8")
	case strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return location{}, errors.New("contains a control character")
	case strings.ContainsRune(s, '\\'):
		return location{}, errors.New("contains a backslash; use '/' as the separator")
	case strings.HasPrefix(s, ":"):
		return location{}, errors.New("names an rclone backend directly; only remotes configured in rclone.conf can be used")
	}

	var loc location
	rest := s
	// rclone reads "name:path" as a remote when the part before the first
	// ':' contains no '/'; anything else is a local path.
	if i := strings.IndexByte(s, ':'); i > 0 && !strings.ContainsRune(s[:i], '/') {
		loc.remote, rest = s[:i], s[i+1:]
		if strings.ContainsRune(loc.remote, ',') {
			return location{}, errors.New("carries rclone connection-string parameters; only remotes configured in rclone.conf can be used")
		}
	}

	abs := strings.HasPrefix(rest, "/")
	var segs []string
	for _, seg := range strings.Split(rest, "/") {
		if seg == "" || seg == "." {
			continue
		}
		if err := checkSegment(seg); err != nil {
			return location{}, err
		}
		segs = append(segs, seg)
	}
	loc.path = strings.Join(segs, "/")
	if abs {
		loc.path = "/" + loc.path
	}
	if loc.remote == "" && !abs {
		return location{}, errors.New("is a relative local path; local paths must be absolute")
	}
	return loc, nil
}

// checkSegment refuses a path segment that is, or decodes to, a parent or
// current-directory reference or a path separator. Percent-encoding is
// undone up to three times, because some backends decode paths again.
func checkSegment(seg string) error {
	v := seg
	for i := 0; i < 4; i++ {
		if dotSegment(v) {
			return fmt.Errorf("contains the segment %q, which can refer to a parent directory", seg)
		}
		if i > 0 && strings.ContainsAny(v, "/\\") {
			return fmt.Errorf("contains the segment %q, which encodes a path separator", seg)
		}
		d, err := url.PathUnescape(v)
		if err != nil || d == v {
			return nil
		}
		v = d
	}
	return fmt.Errorf("contains the segment %q, which is encoded more than three times", seg)
}

// dotSegment reports whether s consists only of dots, dot look-alikes and
// spaces, with at least one dot. Some file systems drop trailing dots and
// spaces, so "...", ".. " and "．．" can all end up meaning "..". A plain "."
// is removed by the caller before this check and refused here only when it
// was encoded.
func dotSegment(s string) bool {
	dot := false
	for _, r := range s {
		switch r {
		case '.', '．', '․', '﹒':
			dot = true
		case ' ', '　':
		default:
			return false
		}
	}
	return dot
}

// within reports whether loc equals root or lies beneath it, comparing the
// normalised form exactly. Case is significant: a differently cased path is
// never treated as inside, even on a backend that ignores case. A relative
// remote path (relative to the remote's home or root) and an absolute one are
// never inside each other.
func within(loc, root location) bool {
	if loc.remote != root.remote || loc.absolute() != root.absolute() {
		return false
	}
	if root.path == "" || root.path == "/" {
		return true
	}
	return loc.path == root.path || strings.HasPrefix(loc.path, root.path+"/")
}

// contained is within plus, for local paths, the same check after resolving
// symbolic links in both, so a link under an allowed root that points
// elsewhere does not pass. Links under a remote alias that points to a local
// folder cannot be resolved here: TrailTransfer does not read rclone.conf.
func contained(loc, root location) bool {
	if !within(loc, root) {
		return false
	}
	if loc.remote != "" {
		return true
	}
	rl, err := resolve(loc.path)
	if err != nil {
		return false
	}
	rr, err := resolve(root.path)
	if err != nil {
		return false
	}
	return within(location{path: rl}, location{path: rr})
}

// resolve resolves every symbolic link in the existing part of the local path
// p. A part that does not exist yet is appended unchanged. A dangling link is
// an error, because creating its target later would redirect the transfer.
func resolve(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if fi, lerr := os.Lstat(p); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symbolic link to a path that does not exist", p)
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p, nil
	}
	rp, err := resolve(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(rp, filepath.Base(p)), nil
}

// quote renders a command value for a rejection message, shortened so that
// the message stays small whatever the command carried.
func quote(s string) string {
	const max = 200
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return fmt.Sprintf("%q", s)
}
