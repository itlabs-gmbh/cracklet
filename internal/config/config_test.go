package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsHonourEnv(t *testing.T) {
	t.Setenv("CRACKLET_HOME", "/custom/cracklet")
	t.Setenv("LIMA_HOME", "/custom/lima")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Home != "/custom/cracklet" || p.LimaHome != "/custom/lima" {
		t.Errorf("unexpected paths: %+v", p)
	}
	if p.LimaSSHConfig("cracklet") != "/custom/lima/cracklet/ssh.config" {
		t.Errorf("LimaSSHConfig = %q", p.LimaSSHConfig("cracklet"))
	}
}

func TestDefaultPathsFallBackToHome(t *testing.T) {
	t.Setenv("CRACKLET_HOME", "")
	t.Setenv("LIMA_HOME", "")
	t.Setenv("HOME", "/Users/test")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Home != "/Users/test/.cracklet" || p.LimaHome != "/Users/test/.lima" {
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
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
