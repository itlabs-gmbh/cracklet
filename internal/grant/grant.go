// Package grant decides whether a microVM may use a capability. Grants are
// the only policy cracklet has: a capability file says how something is
// handed into the guest, a grant says whether. Default is deny.
package grant

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
)

// SSHAgent is a built-in grant with no cap file: it forwards the Mac's SSH
// agent into the guest with `ssh -A`.
const SSHAgent = "ssh-agent"

// Grant allows one capability. There are no scopes: what a capability may
// reach is decided by its credential (a fine-grained GitHub token, say),
// which the upstream enforces, not by the broker reading request paths.
type Grant struct {
	Cap string
}

// Parse reads a capability name.
func Parse(s string) (Grant, error) {
	if name, _, scoped := strings.Cut(s, ":"); scoped && cap.ValidateName(name) == nil {
		return Grant{}, fmt.Errorf("grant %q: capabilities have no scopes; limit the credential instead and use 'cracklet grant VM %s'", s, name)
	}
	if err := cap.ValidateName(s); err != nil {
		return Grant{}, fmt.Errorf("grant %q: %w", s, err)
	}
	return Grant{Cap: s}, nil
}

// String renders the grant in the form Parse accepts.
func (g Grant) String() string {
	return g.Cap
}

// Set is an immutable collection of grants.
type Set []Grant

// ParseSet parses many grants, removing duplicates.
func ParseSet(specs []string) (Set, error) {
	var set Set
	for _, s := range specs {
		g, err := Parse(s)
		if err != nil {
			return nil, err
		}
		set = set.Add(g)
	}
	return set, nil
}

// Add returns a new set containing g (no-op when already present).
func (s Set) Add(g Grant) Set {
	if s.Contains(g) {
		return s
	}
	out := append(append(Set(nil), s...), g)
	sort.Slice(out, func(i, j int) bool { return out[i].Cap < out[j].Cap })
	return out
}

// Remove returns a new set without g.
func (s Set) Remove(g Grant) Set {
	out := make(Set, 0, len(s))
	for _, x := range s {
		if x != g {
			out = append(out, x)
		}
	}
	return out
}

// Contains reports whether g is in the set.
func (s Set) Contains(g Grant) bool {
	return slices.Contains(s, g)
}

// Caps returns the capability names in the set, sorted.
func (s Set) Caps() []string {
	names := make([]string, len(s))
	for i, g := range s {
		names[i] = g.Cap
	}
	return names
}

// Hint is the command that would allow a denied request.
func Hint(vm, name string) string {
	return "cracklet grant " + vm + " " + name
}
