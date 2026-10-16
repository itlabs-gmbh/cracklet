package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// readFile is os.ReadFile as a string, for assertions on generated files.
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

func TestPrepareRunsTheBackendAndWritesTheSSHConfig(t *testing.T) {
	app, _, out := newTestApp(t, sshOnly)
	if err := app.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !backendOf(app).Called("prepare") {
		t.Errorf("backend not prepared:\n%s", backendOf(app).Dump())
	}
	text, err := readFile(app.paths.SSHConfigPath())
	if err != nil {
		t.Fatalf("ssh config should be written: %v", err)
	}
	if !strings.Contains(text, "StrictHostKeyChecking yes") {
		t.Errorf("ssh_config must pin host keys:\n%s", text)
	}
	if !strings.Contains(out.String(), "Include "+app.paths.SSHConfigPath()) {
		t.Errorf("output should explain the ssh Include, got:\n%s", out.String())
	}
}

func TestPrepareStopsWhenTheBackendFails(t *testing.T) {
	app, _, _ := newTestApp(t, sshOnly)
	backendOf(app).PrepareErr = errors.New("host is not ready for skiff")
	if err := app.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("want the backend error, got %v", err)
	}
	if _, err := os.Stat(app.paths.SSHConfigPath()); err == nil {
		t.Error("no ssh_config for a host that is not prepared")
	}
}

func TestDoctorNamesTheBackend(t *testing.T) {
	app, _, out := newTestApp(t, sshOnly)
	backendOf(app).Out = out
	if err := app.Doctor(context.Background()); err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !strings.Contains(out.String(), "backend: fake") || !strings.Contains(out.String(), "fake backend: ok") {
		t.Errorf("output:\n%s", out.String())
	}
}
