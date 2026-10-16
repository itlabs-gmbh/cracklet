package lima

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/runner"
)

// newTestBackend wires a Backend against a fake runner that fails every command.
func newTestBackend(t *testing.T) *Backend {
	t.Helper()
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		return nil, errors.New("unexpected command: " + name + " " + strings.Join(args, " "))
	})
	out := &bytes.Buffer{}
	return New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, out, out)
}

func TestLockSharedAndExclusive(t *testing.T) {
	b := newTestBackend(t)
	first, err := b.HoldForMicroVM()
	if err != nil {
		t.Fatalf("first shared hold: %v", err)
	}
	second, err := b.HoldForMicroVM()
	if err != nil {
		t.Fatalf("microVM starts must not block each other: %v", err)
	}
	if _, err := b.HoldForResize(); err == nil || !strings.Contains(err.Error(), "starting a microVM") {
		t.Fatalf("a resize must not start while a microVM starts, got %v", err)
	}
	first()
	second()

	release, err := b.HoldForResize()
	if err != nil {
		t.Fatalf("exclusive hold after release: %v", err)
	}
	if _, err := b.HoldForMicroVM(); !errors.Is(err, ErrResizing) {
		t.Fatalf("a microVM must not start during a resize, got %v", err)
	}
	if _, err := b.ReadyToBoot(context.Background()); !errors.Is(err, ErrResizing) {
		t.Fatalf("ReadyToBoot must fail before touching limactl, got %v", err)
	}
	release()
	if release, err := b.HoldForMicroVM(); err != nil {
		t.Fatalf("shared hold after the resize: %v", err)
	} else {
		release()
	}
}

func TestWaitForBootHonoursContext(t *testing.T) {
	b := newTestBackend(t)
	release, err := b.WaitForBoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*BootLockPoll)
	defer cancel()
	if _, err := b.WaitForBoot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a held boot lock must wait until the context ends, got %v", err)
	}
	release()
	done := make(chan error, 1)
	go func() {
		r, err := b.WaitForBoot(context.Background())
		if err == nil {
			r()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("boot lock after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("boot lock was not released")
	}
}

func TestNameAndCapabilities(t *testing.T) {
	b := newTestBackend(t)
	if b.Name() != "lima" {
		t.Errorf("Name = %q", b.Name())
	}
	want := backend.Capabilities{Snapshots: true, Profiles: true, Forwards: true, GuestFirewall: true, Resize: true}
	if b.Capabilities() != want {
		t.Errorf("Capabilities = %+v, want %+v", b.Capabilities(), want)
	}
}
