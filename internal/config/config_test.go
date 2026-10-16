package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultPathsHonourEnv(t *testing.T) {
	t.Setenv("SKIFF_HOME", "/custom/skiff")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Home != "/custom/skiff" {
		t.Errorf("unexpected paths: %+v", p)
	}
}

func TestDefaultPathsFallBackToHome(t *testing.T) {
	t.Setenv("SKIFF_HOME", "")
	t.Setenv("HOME", "/Users/test")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if p.Home != "/Users/test/.skiff" {
		t.Errorf("unexpected paths: %+v", p)
	}
}

func TestDerivedPaths(t *testing.T) {
	p := Paths{Home: "/h"}
	cases := map[string]string{
		p.KeyPath():               filepath.Join("/h", "id_ed25519"),
		p.PubKeyPath():            filepath.Join("/h", "id_ed25519.pub"),
		p.SSHConfigPath():         filepath.Join("/h", "ssh_config"),
		p.KnownHostsPath():        filepath.Join("/h", "known_hosts"),
		p.MachinesDir():           filepath.Join("/h", "machines"),
		p.ContainerLockPath():     filepath.Join("/h", "container.lock"),
		p.ContainerBootLockPath(): filepath.Join("/h", "container-boot.lock"),
		p.RelayLockPath():         filepath.Join("/h", "relay.lock"),
		p.RelayLogPath():          filepath.Join("/h", "relay.log"),
		p.SocketPath("vm1"):       filepath.Join("/h", "run", "vm1.sock"),
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
