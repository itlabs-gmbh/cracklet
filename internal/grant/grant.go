// Package grant decides whether a microVM may use a capability. Grants are
// the only policy cracklet has: a capability file says how something is
// handed into the guest, a grant says whether. Default is deny.
package grant

import (
	"fmt"
	"sort"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
)

// Any is the wildcard scope.
const Any = "*"

// SSHAgent is a built-in grant with no cap file: it forwards the Mac's SSH
// agent into the guest with `ssh -A`.
const SSHAgent = "ssh-agent"

// Grant allows one capability, optionally limited to a scope such as org/repo.
type Grant struct {
	Cap   string
	Scope string
}

// Parse reads "cap" or "cap:scope".
func Parse(s string) (Grant, error) {
	name, scope, _ := strings.Cut(s, ":")
	if err := cap.ValidateName(name); err != nil {
		return Grant{}, fmt.Errorf("grant %q: %w", s, err)
	}
	if strings.ContainsAny(scope, " \t\n") {
		return Grant{}, fmt.Errorf("grant %q: scope must not contain whitespace", s)
	}
	return Grant{Cap: name, Scope: scope}, nil
}

// String renders the grant in the form Parse accepts.
func (g Grant) String() string {
	if g.Scope == "" {
		return g.Cap
	}
	return g.Cap + ":" + g.Scope
}

// Set is an immutable collection of grants.
type Set []Grant

// ParseSet parses many grants, rejecting duplicates.
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
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
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

// Contains reports whether exactly g is in the set.
func (s Set) Contains(g Grant) bool {
	for _, x := range s {
		if x == g {
			return true
		}
	}
	return false
}

// Allows reports whether a request for capability name with the given scope
// is covered. An unscoped request needs an unscoped or wildcard grant; a
// scoped request needs the same scope or the wildcard.
func (s Set) Allows(name, scope string) bool {
	for _, g := range s {
		if g.Cap != name {
			continue
		}
		if g.Scope == Any || g.Scope == scope {
			return true
		}
	}
	return false
}

// Caps returns the distinct capability names in the set, sorted.
func (s Set) Caps() []string {
	seen := map[string]bool{}
	var names []string
	for _, g := range s {
		if !seen[g.Cap] {
			seen[g.Cap] = true
			names = append(names, g.Cap)
		}
	}
	sort.Strings(names)
	return names
}

// Strings renders every grant.
func (s Set) Strings() []string {
	out := make([]string, len(s))
	for i, g := range s {
		out[i] = g.String()
	}
	return out
}

// Hint is the command that would allow a denied request.
func Hint(vm, name, scope string) string {
	return "cracklet grant " + vm + " " + Grant{Cap: name, Scope: scope}.String()
}
