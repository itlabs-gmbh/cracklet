// Package shquote turns words into POSIX shell syntax that expands back to
// exactly the same words. ssh has no argv protocol: it joins the remote
// command with spaces and the guest runs the result with "$SHELL -c".
package shquote

import "strings"

// safe lists the characters that never need quoting. "=" and "~" are left
// out on purpose: a leading NAME=value is an assignment and a leading "~" is
// tilde-expanded.
const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./:@%+,"

// Quote returns s as a single shell word. Words made of safe characters stay
// readable; everything else is single-quoted, and an embedded quote closes the
// quoting, adds an escaped quote and reopens it.
func Quote(s string) string {
	if s != "" && strings.Trim(s, safe) == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Join quotes every word of argv and joins them with spaces.
func Join(argv []string) string {
	quoted := make([]string, len(argv))
	for i, w := range argv {
		quoted[i] = Quote(w)
	}
	return strings.Join(quoted, " ")
}
