package grant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAndString(t *testing.T) {
	for in, want := range map[string]Grant{
		"claude":          {Cap: "claude"},
		"github:org/repo": {Cap: "github", Scope: "org/repo"},
		"github:*":        {Cap: "github", Scope: "*"},
		"a:b:c":           {Cap: "a", Scope: "b:c"},
	} {
		g, err := Parse(in)
		if err != nil || g != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, g, err, want)
		}
		if g.String() != in {
			t.Errorf("String() = %q, want %q", g.String(), in)
		}
	}
	for _, bad := range []string{"", "Bad", "x:a b", ":scope"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestSetOperationsAreImmutable(t *testing.T) {
	base, err := ParseSet([]string{"github:org/repo", "claude", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 2 || base[0].Cap != "claude" {
		t.Fatalf("ParseSet should dedupe and sort, got %v", base.Strings())
	}
	added := base.Add(Grant{Cap: "chrome-devtools"})
	if len(base) != 2 || len(added) != 3 {
		t.Errorf("Add must not mutate: base=%v added=%v", base.Strings(), added.Strings())
	}
	removed := added.Remove(Grant{Cap: "claude"})
	if len(added) != 3 || removed.Contains(Grant{Cap: "claude"}) {
		t.Errorf("Remove must not mutate: added=%v removed=%v", added.Strings(), removed.Strings())
	}
	if got := added.Caps(); strings.Join(got, ",") != "chrome-devtools,claude,github" {
		t.Errorf("Caps = %v", got)
	}
}

func TestAllows(t *testing.T) {
	set, _ := ParseSet([]string{"claude", "github:org/repo", "gitlab:*"})
	cases := []struct {
		name, scope string
		want        bool
	}{
		{"claude", "", true},
		{"claude", "x", false},
		{"github", "org/repo", true},
		{"github", "org/other", false},
		{"github", "", false},
		{"gitlab", "anything", true},
		{"gitlab", "", true},
		{"nope", "", false},
	}
	for _, tc := range cases {
		if got := set.Allows(tc.name, tc.scope); got != tc.want {
			t.Errorf("Allows(%q,%q) = %v, want %v", tc.name, tc.scope, got, tc.want)
		}
	}
	if Hint("vm1", "github", "o/r") != "cracklet grant vm1 github:o/r" {
		t.Errorf("Hint = %q", Hint("vm1", "github", "o/r"))
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if set, err := s.Load("vm1"); err != nil || len(set) != 0 {
		t.Fatalf("fresh VM should have no grants, got %v, %v", set, err)
	}
	set, _ := ParseSet([]string{"github:o/r", "claude"})
	if err := s.Save("vm1", set); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("vm1")
	if err != nil || strings.Join(got.Strings(), ",") != "claude,github:o/r" {
		t.Fatalf("Load = %v, %v", got, err)
	}
	info, _ := os.Stat(filepath.Join(s.Dir, "vm1", "grants"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("grants file mode = %o", info.Mode().Perm())
	}
	tok1, err := s.Token("vm1")
	if err != nil || len(tok1) != 32 {
		t.Fatalf("Token = %q, %v", tok1, err)
	}
	tok2, _ := s.Token("vm1")
	if tok1 != tok2 {
		t.Errorf("token must be stable, got %q then %q", tok1, tok2)
	}
	other, _ := s.Token("vm2")
	if other == tok1 {
		t.Errorf("tokens must differ per VM")
	}
	if err := s.Remove("vm1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "vm1")); !os.IsNotExist(err) {
		t.Errorf("Remove should delete the directory")
	}
}

func TestStoreRejectsBadNames(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, bad := range []string{"", "../x", "Bad"} {
		if _, err := s.Load(bad); err == nil {
			t.Errorf("Load(%q) should fail", bad)
		}
		if err := s.Remove(bad); err == nil {
			t.Errorf("Remove(%q) should fail", bad)
		}
	}
}

func TestStoreRejectsCorruptGrants(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	_ = os.MkdirAll(filepath.Join(s.Dir, "vm1"), 0o700)
	_ = os.WriteFile(filepath.Join(s.Dir, "vm1", "grants"), []byte("Bad Grant\n"), 0o600)
	if _, err := s.Load("vm1"); err == nil {
		t.Errorf("corrupt grants must fail loudly")
	}
}
