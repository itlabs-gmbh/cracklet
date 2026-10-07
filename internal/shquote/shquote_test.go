package shquote

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"ls":           "ls",
		"-la":          "-la",
		"/usr/bin/env": "/usr/bin/env",
		"":             "''",
		"a b":          "'a b'",
		"it's":         `'it'\''s'`,
		"$HOME":        `'$HOME'`,
		"FOO=bar":      "'FOO=bar'",
		"~":            "'~'",
		"*.go":         "'*.go'",
		"a\nb":         "'a\nb'",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinRoundTripsThroughSh(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	argv := []string{
		"plain", "", "two words", "it's", `"double"`, "$HOME", "$(id)", "`id`",
		"a;b", "a|b", "a&&b", "*", "~", "FOO=bar", `back\slash`, "tab\there",
		"new\nline", "-n", "--", "{a,b}", "!", "#comment", "ünïcode",
	}
	script := "for a do printf '%s\\0' \"$a\"; done"
	// sh -c SCRIPT NAME ARGS... mimics the remote side: ssh hands the guest
	// shell one string, which must expand back to exactly argv.
	out, err := exec.Command(sh, "-c", "set -- "+Join(argv)+"; "+script).Output()
	if err != nil {
		t.Fatalf("sh: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if !reflect.DeepEqual(got, argv) {
		t.Errorf("round trip mismatch:\n got %q\nwant %q", got, argv)
	}
}

func TestJoinEmpty(t *testing.T) {
	if got := Join(nil); got != "" {
		t.Errorf("Join(nil) = %q, want empty", got)
	}
}
