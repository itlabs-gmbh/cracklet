package sshcfg

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	out, err := Render(Options{
		Instance:       "skiff",
		KeyPath:        "/Users/me/.skiff/id_ed25519",
		ProxyCommand:   "/usr/local/bin/skiff _dial %h %p",
		KnownHostsFile: "/Users/me/.skiff/known_hosts",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"Host *.skiff",
		"User root",
		"IdentityFile /Users/me/.skiff/id_ed25519",
		"ProxyCommand /usr/local/bin/skiff _dial %h %p",
		"StrictHostKeyChecking yes",
		"UserKnownHostsFile /Users/me/.skiff/known_hosts",
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
	if _, err := Render(Options{Instance: "x", KeyPath: "/k", ProxyCommand: "evil\nHost *", KnownHostsFile: "/kh"}); err == nil {
		t.Error("a line break in a value must be rejected: it would inject config")
	}
	if _, err := Render(Options{Instance: "x", KeyPath: "/k", ProxyCommand: "p"}); err == nil {
		t.Error("without a known_hosts file nothing pins host keys; it is required")
	}
}

func TestHostAliasRoundTrip(t *testing.T) {
	if got := HostAlias("vm1", "skiff"); got != "vm1.skiff" {
		t.Errorf("HostAlias = %q", got)
	}
	for host, want := range map[string]string{"vm1.skiff": "vm1", "vm1": "vm1", "a.b.skiff": "a.b"} {
		if got := VMName(host, "skiff"); got != want {
			t.Errorf("VMName(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestRenderPinsHostKeys(t *testing.T) {
	out, err := Render(Options{
		Instance:       "skiff",
		KeyPath:        "/Users/me/.skiff/id_ed25519",
		ProxyCommand:   "/usr/local/bin/skiff _dial %h %p",
		KnownHostsFile: "/Users/me/.skiff/known_hosts",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{"StrictHostKeyChecking yes", "UserKnownHostsFile /Users/me/.skiff/known_hosts"} {
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
