package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/agent"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// failingAt wraps a handler and fails the first invocation whose command line
// contains needle, so each error branch can be exercised in isolation.
func failingAt(base runner.FakeHandler, needle string) runner.FakeHandler {
	return func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		if strings.Contains(joined, needle) {
			return nil, errors.New("injected failure at " + needle)
		}
		return base(name, args)
	}
}

func TestPrepareErrorBranches(t *testing.T) {
	cases := map[string]string{
		"ssh-keygen":      "ssh-keygen",
		"instance create": "limactl start --tty=false --name",
		"agent probe":     "sha256sum",
		"key upload":      "sh " + config.GuestKeyPath + " 0600",
		"agent prepare":   config.AgentPath + " prepare",
		"brew install":    "brew install lima",
	}
	for label, needle := range cases {
		t.Run(label, func(t *testing.T) {
			hasLima := needle != "brew install lima"
			app, _, _, paths := prepareApp(t, failingAt(hostHandler("", agent.Checksum()), needle), hasLima)
			if needle != "ssh-keygen" {
				writeDummyKeys(t, paths)
			}
			err := app.Prepare(context.Background(), PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40})
			if err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("expected injected failure to surface, got %v", err)
			}
		})
	}
}

func TestPrepareStartFailureSurfaces(t *testing.T) {
	app, _, _, paths := prepareApp(t, failingAt(hostHandler("Stopped", agent.Checksum()), "limactl start --tty=false cracklet"), true)
	writeDummyKeys(t, paths)
	if err := app.Prepare(context.Background(), PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40}); err == nil {
		t.Fatal("expected start failure")
	}
}

func TestListFailsOnGarbageFromAgent(t *testing.T) {
	app, _, _ := newTestApp(t, defaultHandler(map[string]string{"ls": "not json"}))
	if _, err := app.ListVMs(context.Background()); err == nil || !strings.Contains(err.Error(), "parse agent output") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestNewFailsOnGarbageFromAgent(t *testing.T) {
	app, _, _ := newTestApp(t, defaultHandler(map[string]string{"new": "{"}))
	if _, err := app.NewVM(context.Background(), vm.Spec{Name: "vm1", VCPUs: 1, MemMiB: 256, Disk: "2G"}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLifecycleSurfacesAgentErrors(t *testing.T) {
	app, _, _ := newTestApp(t, defaultHandler(nil)) // no agent responses configured
	if _, err := app.StartVM(context.Background(), "vm1"); err == nil {
		t.Error("start should surface the agent error")
	}
	if _, err := app.StopVM(context.Background(), "vm1"); err == nil {
		t.Error("stop should surface the agent error")
	}
	if err := app.RemoveVM(context.Background(), "vm1"); err == nil {
		t.Error("rm should surface the agent error")
	}
	if _, err := app.ListVMs(context.Background()); err == nil {
		t.Error("ls should surface the agent error")
	}
}

func TestCommandsFailWhenInstanceMissing(t *testing.T) {
	app, _, _ := newTestApp(t, func(string, []string) ([]byte, error) { return []byte(""), nil })
	for label, err := range map[string]error{
		"rm":  app.RemoveVM(context.Background(), "vm1"),
		"ssh": app.SSH(context.Background(), "vm1", nil),
	} {
		if err == nil || !strings.Contains(err.Error(), "cracklet prepare") {
			t.Errorf("%s: expected hint to run cracklet prepare, got %v", label, err)
		}
	}
	if _, err := app.ListVMs(context.Background()); err == nil {
		t.Error("ls: expected error when the instance is missing")
	}
}

func TestListFailsWhenLimactlFails(t *testing.T) {
	app, _, _ := newTestApp(t, func(string, []string) ([]byte, error) { return nil, errors.New("limactl exploded") })
	if _, err := app.ListVMs(context.Background()); err == nil || !strings.Contains(err.Error(), "limactl exploded") {
		t.Fatalf("expected limactl error, got %v", err)
	}
}

func TestRemoteExitErrorMessage(t *testing.T) {
	if got := (&RemoteExitError{Code: 4}).Error(); !strings.Contains(got, "4") {
		t.Errorf("unexpected message %q", got)
	}
	if got := (&RemoteExitError{Code: 143, Reason: "killed"}).Error(); got != "killed" {
		t.Errorf("reason should win, got %q", got)
	}
}

func TestNewCleansUpWhenInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app, fake, out := newTestApp(t, func(name string, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, config.AgentPath+" new "):
			cancel() // Ctrl-C while the agent creates the VM
			return nil, errors.New("signal: interrupt")
		case strings.Contains(joined, config.AgentPath+" rm "):
			return nil, nil
		}
		return defaultHandler(nil)(name, args)
	})
	if _, err := app.NewVM(ctx, vm.Spec{Name: "doomed", VCPUs: 1, MemMiB: 256, Disk: "2G"}); err == nil {
		t.Fatal("expected the interrupted creation to fail")
	}
	if !fake.CalledWithSuffix(config.AgentPath + " rm doomed") {
		t.Errorf("expected a best-effort rm after cancellation:\n%s", fake.Dump())
	}
	if !strings.Contains(out.String(), "interrupted") {
		t.Errorf("user should be told about the cleanup:\n%s", out.String())
	}
}

func TestNewDoesNotCleanUpOnOrdinaryFailure(t *testing.T) {
	app, fake, _ := newTestApp(t, func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), config.AgentPath+" new ") {
			return nil, errors.New("exit status 1")
		}
		return defaultHandler(nil)(name, args)
	})
	if _, err := app.NewVM(context.Background(), vm.Spec{Name: "x", VCPUs: 1, MemMiB: 256, Disk: "2G"}); err == nil {
		t.Fatal("expected failure")
	}
	if fake.CalledWithSuffix(config.AgentPath + " rm x") {
		t.Error("an ordinary agent failure already rolled back inside the agent; no rm expected")
	}
}

func TestNewInterruptedWithoutNameOnlyHints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	app, fake, out := newTestApp(t, func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), config.AgentPath+" new ") {
			cancel() // Ctrl-C while the agent creates the VM
			return nil, errors.New("signal: interrupt")
		}
		return defaultHandler(nil)(name, args)
	})
	_, _ = app.NewVM(ctx, vm.Spec{VCPUs: 1, MemMiB: 256, Disk: "2G"})
	if fake.CalledWithSuffix(" rm -") || !strings.Contains(out.String(), "cracklet ls") {
		t.Errorf("unnamed VMs cannot be cleaned up blindly:\n%s\n%s", fake.Dump(), out.String())
	}
}
