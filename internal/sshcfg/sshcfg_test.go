package sshcfg

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	out, err := Render(Options{
		Instance:      "cracklet",
		KeyPath:       "/Users/me/.cracklet/id_ed25519",
		LimaSSHConfig: "/Users/me/.lima/cracklet/ssh.config",
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
}

func TestHostAlias(t *testing.T) {
	if got := HostAlias("vm1", "cracklet"); got != "vm1.cracklet" {
		t.Errorf("HostAlias = %q", got)
	}
}
