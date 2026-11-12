package logging

import (
	"strings"
	"testing"
)

// The forms SECURITY.md says are masked. The filter is a pattern for
// "key=value" and "key: value" assignments; SECURITY.md lists the forms it
// does not catch.
func TestRedactMasksCredentialAssignments(t *testing.T) {
	for in, secret := range map[string]string{
		"Failed to connect: password=hunter2 for user demo": "hunter2",
		"pass: hunter2":                        "hunter2",
		"secret=s3cr3t":                        "s3cr3t",
		"token: abc.def.ghi":                   "abc.def.ghi",
		"api_key=AKIA0000":                     "AKIA0000",
		"X-Amz-Credential=AKIA123/20261108/eu": "AKIA123/20261108/eu",
		"key = 0123456789abcdef":               "0123456789abcdef",
		"credential:xyz":                       "xyz",
	} {
		out := Redact(in)
		if strings.Contains(out, secret) || !strings.Contains(out, "***") {
			t.Errorf("Redact(%q) = %q, want %q masked", in, out, secret)
		}
	}
}

func TestRedactLeavesTextWithoutAssignmentsAlone(t *testing.T) {
	for _, in := range []string{
		"directory not found",
		"Failed to copy: object not found",
		"",
	} {
		if out := Redact(in); out != in {
			t.Errorf("Redact(%q) = %q, want it unchanged", in, out)
		}
	}
}
