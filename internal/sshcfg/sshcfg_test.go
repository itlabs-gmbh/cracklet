package sshcfg

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	out, err := Render(Options{
		Instance:     "cracklet",
		KeyPath:      "/Users/me/.cracklet/id_ed25519",
		ProxyCommand: "ssh -F /Users/me/.lima/cracklet/ssh.config -W %h:%p lima-cracklet",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"Host *.cracklet",
		"User root",
		"IdentityFile /Users/me/.cracklet/id_ed25519",
		"ProxyCommand ssh -F /Users/me/.lima/cracklet/ssh.config -W %h:%p lima-cracklet",
		"StrictHostKeyChecking no",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderRequiresFields(t *testing.T) {
	if _, err := Render(Options{}); err == nil {
		t.Error("expected error for empty options")
	}
	if _, err := Render(Options{Instance: "x", KeyPath: "/k", ProxyCommand: "evil\nHost *"}); err == nil {
		t.Error("a line break in a value must be rejected: it would inject config")
	}
}

func TestHostAliasRoundTrip(t *testing.T) {
	if got := HostAlias("vm1", "cracklet"); got != "vm1.cracklet" {
		t.Errorf("HostAlias = %q", got)
	}
	for host, want := range map[string]string{"vm1.cracklet": "vm1", "vm1": "vm1", "a.b.cracklet": "a.b"} {
		if got := VMName(host, "cracklet"); got != want {
			t.Errorf("VMName(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestRenderPinsHostKeysWhenAKnownHostsFileIsGiven(t *testing.T) {
	out, err := Render(Options{
		Instance:       "cracklet",
		KeyPath:        "/Users/me/.cracklet/id_ed25519",
		ProxyCommand:   "/usr/local/bin/cracklet _dial %h %p",
		KnownHostsFile: "/Users/me/.cracklet/known_hosts",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"StrictHostKeyChecking yes", "UserKnownHostsFile /Users/me/.cracklet/known_hosts"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"StrictHostKeyChecking no", "/dev/null"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, out)
		}
	}
	if _, err := Render(Options{Instance: "x", KeyPath: "/k", ProxyCommand: "p", KnownHostsFile: "/a\nHost *"}); err == nil {
		t.Error("a line break in the known_hosts path must be rejected")
	}
}
