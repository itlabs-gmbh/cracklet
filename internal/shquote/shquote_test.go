package shquote

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestJoinAlwaysQuotesCommandName(t *testing.T) {
	if got, want := Join([]string{"time", "-p", "true"}), "'time' -p true"; got != want {
		t.Errorf("Join = %q, want %q", got, want)
	}
}

// Reserved words are only recognised unquoted in command position, so a
// program that happens to be called "time" or "if" must still be executed.
func TestJoinRunsProgramsNamedLikeKeywords(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on PATH")
	}
	keywords := []string{"time", "if", "then", "for", "while", "until", "case", "do", "function", "select", "coproc", "!", "{", "[["}
	dir := t.TempDir()
	stub := []byte("#!/bin/sh\nprintf '%s|' \"${0##*/}\" \"$@\"\n")
	for _, kw := range keywords {
		if err := os.WriteFile(filepath.Join(dir, kw), stub, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, kw := range keywords {
		cmd := exec.Command(bash, "-c", Join([]string{kw, "-f", "%e", "x"}))
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if want := kw + "|-f|%e|x|"; err != nil || string(out) != want {
			t.Errorf("%q: got %q (err %v), want %q", kw, out, err, want)
		}
	}
}

func TestJoinEmpty(t *testing.T) {
	if got := Join(nil); got != "" {
		t.Errorf("Join(nil) = %q, want empty", got)
	}
}
