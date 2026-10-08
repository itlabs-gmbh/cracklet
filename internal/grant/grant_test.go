package grant

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseAndString(t *testing.T) {
	g, err := Parse("claude")
	if err != nil || g != (Grant{Cap: "claude"}) || g.String() != "claude" {
		t.Errorf("Parse(claude) = %+v, %v", g, err)
	}
	for _, bad := range []string{"", "Bad", "x y", ":scope"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestParseRejectsScopes(t *testing.T) {
	for _, in := range []string{"github:*", "github:org/repo"} {
		_, err := Parse(in)
		if err == nil || !strings.Contains(err.Error(), "cracklet grant VM github") {
			t.Errorf("Parse(%q) = %v; want an error that names the plain grant", in, err)
		}
	}
}

func TestSetOperationsAreImmutable(t *testing.T) {
	base, err := ParseSet([]string{"github", "claude", "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 2 || base[0].Cap != "claude" {
		t.Fatalf("ParseSet should dedupe and sort, got %v", base.Caps())
	}
	added := base.Add(Grant{Cap: "chrome-devtools"})
	if len(base) != 2 || len(added) != 3 {
		t.Errorf("Add must not mutate: base=%v added=%v", base.Caps(), added.Caps())
	}
	removed := added.Remove(Grant{Cap: "claude"})
	if len(added) != 3 || removed.Contains(Grant{Cap: "claude"}) {
		t.Errorf("Remove must not mutate: added=%v removed=%v", added.Caps(), removed.Caps())
	}
	if got := added.Caps(); strings.Join(got, ",") != "chrome-devtools,claude,github" {
		t.Errorf("Caps = %v", got)
	}
	if !added.Contains(Grant{Cap: "github"}) || added.Contains(Grant{Cap: "gitlab"}) {
		t.Errorf("Contains is wrong for %v", added.Caps())
	}
	if Hint("vm1", "github") != "cracklet grant vm1 github" {
		t.Errorf("Hint = %q", Hint("vm1", "github"))
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if set, err := s.Load("vm1"); err != nil || len(set) != 0 {
		t.Fatalf("fresh VM should have no grants, got %v, %v", set, err)
	}
	set, _ := ParseSet([]string{"github", "claude"})
	if err := s.Save("vm1", set); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load("vm1")
	if err != nil || strings.Join(got.Caps(), ",") != "claude,github" {
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
	if _, err := os.Stat(filepath.Join(s.Dir, "vm1.lock")); err != nil {
		t.Errorf("Remove must keep the lock file so holders stay exclusive: %v", err)
	}
}

func TestStoreLockIsExclusiveAcrossHolders(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	unlock, err := s.Lock("vm1")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	acquired := make(chan struct{})
	go func() {
		second, err := s.Lock("vm1")
		if err != nil {
			t.Error(err)
		}
		close(acquired)
		second()
	}()
	select {
	case <-acquired:
		t.Fatal("second lock must wait for the first")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock should proceed after unlock")
	}
	if _, err := s.Lock("Bad"); err == nil {
		t.Errorf("Lock must validate the name")
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

func TestStoreNamesListsVMsWithState(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "vms")}
	if names, err := s.Names(); err != nil || len(names) != 0 {
		t.Fatalf("a missing store has no VMs, got %v, %v", names, err)
	}
	for _, vm := range []string{"web", "api"} {
		if _, err := s.Token(vm); err != nil {
			t.Fatal(err)
		}
	}
	unlock, err := s.Lock("gone") // leaves gone.lock behind, which is no VM state
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := os.Mkdir(filepath.Join(s.Dir, "Not_A_VM"), 0o700); err != nil {
		t.Fatal(err)
	}
	names, err := s.Names()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "api,web" {
		t.Errorf("Names() = %v, want [api web]", names)
	}
}

func TestStorePruneRemovesOnlyVanishedVMs(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	for _, vm := range []string{"old", "reborn"} {
		if err := s.Save(vm, Set{{Cap: "claude"}}); err != nil {
			t.Fatal(err)
		}
	}
	alive := func() (map[string]bool, error) {
		// The lock must be held here, otherwise a grant could slip in
		// between this check and the removal.
		f, err := os.OpenFile(filepath.Join(s.Dir, "old.lock"), os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("lock file missing: %v", err)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			t.Error("Prune must hold the VM's lock while it checks liveness")
		}
		return map[string]bool{"reborn": true}, nil
	}
	removed, err := s.Prune([]string{"old", "reborn"}, alive)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if strings.Join(removed, ",") != "old" {
		t.Errorf("removed %v, want [old]", removed)
	}
	if set, _ := s.Load("old"); len(set) != 0 {
		t.Errorf("state of old should be gone, got %v", set)
	}
	if set, _ := s.Load("reborn"); len(set) != 1 {
		t.Errorf("a VM that exists again keeps its grants, got %v", set)
	}
}

func TestStorePruneKeepsEverythingWhenLivenessFails(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := s.Save("old", Set{{Cap: "claude"}}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Prune([]string{"old"}, func() (map[string]bool, error) { return nil, errors.New("agent down") })
	if err == nil || len(removed) != 0 {
		t.Fatalf("Prune = %v, %v; want an error and nothing removed", removed, err)
	}
	if set, _ := s.Load("old"); len(set) != 1 {
		t.Error("state must survive a failed liveness check")
	}
	if _, err := s.Prune([]string{"../etc"}, func() (map[string]bool, error) { return nil, nil }); err == nil {
		t.Error("Prune must reject invalid names")
	}
}

func TestStoreWithFreshState(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := s.Save("box", Set{{Cap: "claude"}}); err != nil {
		t.Fatal(err)
	}
	err := s.WithFreshState("box", func() error {
		f, err := os.OpenFile(filepath.Join(s.Dir, "box.lock"), os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("lock file missing: %v", err)
		}
		defer f.Close()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			t.Error("create must run under the VM's lock, so a grant cannot slip in before the clear")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithFreshState: %v", err)
	}
	if set, _ := s.Load("box"); len(set) != 0 {
		t.Errorf("state of the predecessor must be gone, got %v", set)
	}

	if err := s.Save("box", Set{{Cap: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.WithFreshState("box", func() error { return errors.New("already exists") }); err == nil {
		t.Fatal("the create error must be returned")
	}
	if set, _ := s.Load("box"); len(set) != 1 {
		t.Error("a failed create must leave the state of the existing VM alone")
	}
}
