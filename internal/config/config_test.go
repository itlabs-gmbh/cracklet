package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsHonourEnv(t *testing.T) {
	t.Setenv("SKIFF_HOME", "/custom/skiff")
	t.Setenv("LIMA_HOME", "/custom/lima")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Home != "/custom/skiff" || p.LimaHome != "/custom/lima" {
		t.Errorf("unexpected paths: %+v", p)
	}
	if p.LimaSSHConfig("skiff") != "/custom/lima/skiff/ssh.config" {
		t.Errorf("LimaSSHConfig = %q", p.LimaSSHConfig("skiff"))
	}
}

func TestDefaultPathsFallBackToHome(t *testing.T) {
	t.Setenv("SKIFF_HOME", "")
	t.Setenv("LIMA_HOME", "")
	t.Setenv("HOME", "/Users/test")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Home != "/Users/test/.skiff" || p.LimaHome != "/Users/test/.lima" {
		t.Errorf("unexpected paths: %+v", p)
	}
}

func TestDerivedPaths(t *testing.T) {
	p := Paths{Home: "/h"}
	cases := map[string]string{
		p.KeyPath():          filepath.Join("/h", "id_ed25519"),
		p.PubKeyPath():       filepath.Join("/h", "id_ed25519.pub"),
		p.SSHConfigPath():    filepath.Join("/h", "ssh_config"),
		p.LimaTemplatePath(): filepath.Join("/h", "lima.yaml"),
		p.LimaLockPath():     filepath.Join("/h", "lima.lock"),
		p.LimaBootLockPath(): filepath.Join("/h", "lima-boot.lock"),
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestKnownHostsPath(t *testing.T) {
	p := Paths{Home: "/h"}
	if got := p.KnownHostsPath(); got != filepath.Join("/h", "known_hosts") {
		t.Errorf("KnownHostsPath = %q", got)
	}
}

func TestRelayPaths(t *testing.T) {
	p := Paths{Home: "/h"}
	if p.RelayLockPath() != "/h/relay.lock" || p.RelayLogPath() != "/h/relay.log" {
		t.Errorf("paths: %q %q", p.RelayLockPath(), p.RelayLogPath())
	}
}
