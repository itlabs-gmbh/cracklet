package secret

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

func newResolver(fake *runner.Fake) *Resolver {
	r := NewResolver(fake)
	r.LookupEnv = func(k string) (string, bool) {
		if k == "TOK" {
			return "from-env", true
		}
		return "", false
	}
	r.ReadFile = func(p string) ([]byte, error) {
		if p == "/tmp/t" {
			return []byte("from-file\n"), nil
		}
		return nil, errors.New("no such file")
	}
	return r
}

func TestResolveSchemes(t *testing.T) {
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		switch {
		case name == "security" && args[0] == "find-generic-password":
			return []byte("from-keychain\n"), nil
		case name == "sh":
			return []byte("from-cmd\n"), nil
		}
		return nil, errors.New("unexpected " + name)
	})
	r := newResolver(fake)
	cases := map[string]string{
		"keychain:cracklet/claude-token": "from-keychain",
		"env:TOK":                        "from-env",
		"cmd:op read x":                  "from-cmd",
		"file:/tmp/t":                    "from-file",
	}
	for ref, want := range cases {
		got, err := r.Resolve(context.Background(), ref)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	if !fake.Called("security find-generic-password -s cracklet -a claude-token -w") {
		t.Errorf("unexpected security invocation:\n%s", fake.Dump())
	}
}

func TestResolveErrors(t *testing.T) {
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		if name == "sh" {
			return []byte("\n"), nil
		}
		return nil, errors.New("not found")
	})
	r := newResolver(fake)
	for ref, want := range map[string]string{
		"bogus":                "must look like scheme:value",
		"vault:x":              "unknown scheme",
		"keychain:noslash":     "keychain:<service>/<account>",
		"keychain:cracklet/zz": "cracklet secret set zz",
		"env:MISSING":          "is not set",
		"file:/nope":           "no such file",
		"cmd:echo":             "empty value",
	} {
		_, err := r.Resolve(context.Background(), ref)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(%q): want %q, got %v", ref, want, err)
		}
	}
}

func TestResolveCachesFailuresBriefly(t *testing.T) {
	calls := 0
	fake := runner.NewFake(func(string, []string) ([]byte, error) {
		calls++
		return nil, errors.New("no keychain")
	})
	now := time.Unix(1000, 0)
	r := newResolver(fake)
	r.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, err := r.Resolve(context.Background(), "keychain:cracklet/x"); err == nil {
			t.Fatal("expected failure")
		}
	}
	if calls != 1 {
		t.Errorf("failures should be cached briefly, got %d calls", calls)
	}
	now = now.Add(10 * time.Second)
	_, _ = r.Resolve(context.Background(), "keychain:cracklet/x")
	if calls != 2 {
		t.Errorf("failure cache should expire, got %d calls", calls)
	}
}

func TestResolveCachesWithinTTL(t *testing.T) {
	calls := 0
	fake := runner.NewFake(func(string, []string) ([]byte, error) {
		calls++
		return []byte("v"), nil
	})
	now := time.Unix(1000, 0)
	r := newResolver(fake)
	r.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if _, err := r.Resolve(context.Background(), "cmd:x"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Errorf("expected one command run within TTL, got %d", calls)
	}
	now = now.Add(2 * time.Minute)
	if _, err := r.Resolve(context.Background(), "cmd:x"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected refetch after TTL, got %d calls", calls)
	}
}

func TestStoreAndDelete(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	if err := Store(context.Background(), fake, "claude-token", `s3"c\ret`); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !fake.Called("security -i") {
		t.Errorf("value must not appear in argv:\n%s", fake.Dump())
	}
	if in, ok := fake.Input("security -i"); !ok || in != `add-generic-password -U -s cracklet -a claude-token -w "s3\"c\\ret"`+"\n" {
		t.Errorf("command should travel via stdin with the value quoted, got %q", in)
	}
	for name, value := range map[string]string{"x": "  ", "y": "two\nlines", "Bad Name": "v"} {
		if err := Store(context.Background(), fake, name, value); err == nil {
			t.Errorf("Store(%q, %q) must be rejected", name, value)
		}
	}
	if err := Delete(context.Background(), fake, "claude-token"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !fake.Called("security delete-generic-password -s cracklet -a claude-token") {
		t.Errorf("unexpected calls:\n%s", fake.Dump())
	}
}
