package sshkey

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/runner"
)

func TestEnsureGeneratesMissingKey(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	paths := config.Paths{Home: filepath.Join(t.TempDir(), "skiff")}
	out := &bytes.Buffer{}
	if err := Ensure(context.Background(), fake, paths, out); err != nil {
		t.Fatal(err)
	}
	if !fake.CalledWithSuffix("-f " + paths.KeyPath()) {
		t.Errorf("expected ssh-keygen for %s, got:\n%s", paths.KeyPath(), fake.Dump())
	}
	if !strings.Contains(out.String(), "Generating SSH key") {
		t.Errorf("progress missing: %q", out.String())
	}
	if _, err := os.Stat(paths.Home); err != nil {
		t.Errorf("home directory not created: %v", err)
	}
}

func TestEnsureKeepsExistingKey(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	paths := config.Paths{Home: t.TempDir()}
	if err := os.WriteFile(paths.KeyPath(), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), fake, paths, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("an existing key must not be regenerated:\n%s", fake.Dump())
	}
}

func TestEnsureWrapsKeygenFailure(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, errors.New("boom") })
	paths := config.Paths{Home: t.TempDir()}
	err := Ensure(context.Background(), fake, paths, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "generate ssh key") {
		t.Fatalf("want wrapped keygen error, got %v", err)
	}
}

func TestEnsureReportsUnreadableKey(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	// A file where the home directory should be makes Stat fail with ENOTDIR.
	home := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(home, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Ensure(context.Background(), fake, config.Paths{Home: home}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "check ssh key") {
		t.Fatalf("want check error, got %v", err)
	}
}
