package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// sshFailing answers every ssh call with err; nothing else may run.
func sshFailing(err error) func(string, []string) ([]byte, error) {
	return func(name string, args []string) ([]byte, error) {
		if name == "ssh" {
			return nil, err
		}
		return sshOnly(name, args)
	}
}

func TestSSHExplainsConnectionFailures(t *testing.T) {
	transportErr := exec.Command("sh", "-c", "exit 255").Run()
	app, _, _ := newTestApp(t, sshFailing(fmt.Errorf("ssh: %w", transportErr)))
	err := app.SSH(context.Background(), "vm1", nil)
	if err == nil || !strings.Contains(err.Error(), "skiff ls") || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("expected hints at 'skiff ls' and the pinned host key, got %v", err)
	}
}

func TestSSHPassesRemoteExitCodeThrough(t *testing.T) {
	remoteErr := exec.Command("sh", "-c", "exit 3").Run()
	app, _, _ := newTestApp(t, sshFailing(remoteErr))
	err := app.SSH(context.Background(), "vm1", []string{"false"})
	var remote *RemoteExitError
	if !errors.As(err, &remote) || remote.Code != 3 || remote.Reason != "" {
		t.Fatalf("expected silent RemoteExitError with code 3, got %v", err)
	}
}

func TestSSHReportsSignalDeath(t *testing.T) {
	signalErr := exec.Command("sh", "-c", "kill -TERM $$").Run()
	app, _, _ := newTestApp(t, sshFailing(signalErr))
	err := app.SSH(context.Background(), "vm1", nil)
	var remote *RemoteExitError
	if !errors.As(err, &remote) || remote.Code != 128+15 || !strings.Contains(remote.Reason, "signal") {
		t.Fatalf("expected 128+SIGTERM with a reason, got %v", err)
	}
}
