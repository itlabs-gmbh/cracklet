package host

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

func TestGatherUsesRunnerAndLookPath(t *testing.T) {
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		switch name {
		case "sw_vers":
			return []byte("26.7\n"), nil
		case "sysctl":
			if args[1] == "kern.hv_support" {
				return []byte("1\n"), nil
			}
			return []byte("Apple M4 Pro\n"), nil
		}
		return nil, errors.New("unexpected " + name)
	})
	g := Gatherer{OS: "darwin", Arch: "arm64", Runner: fake, LookPath: func(name string) (string, error) {
		if name == "limactl" {
			return "", errors.New("missing")
		}
		return "/opt/homebrew/bin/" + name, nil
	}}
	f := g.Gather(context.Background())
	if f.OS != "darwin" || f.Arch != "arm64" {
		t.Errorf("configured platform must be reported, got %s/%s", f.OS, f.Arch)
	}
	if f.MacOSVersion != "26.7" || f.Chip != "Apple M4 Pro" || !f.HVSupport || !f.HasBrew || f.HasLima {
		t.Errorf("unexpected facts: %+v", f)
	}
}

func TestGatherDefaultsToRuntimePlatform(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, errors.New("nope") })
	g := Gatherer{Runner: fake, LookPath: func(string) (string, error) { return "", errors.New("nope") }}
	f := g.Gather(context.Background())
	if f.OS != runtime.GOOS || f.Arch != runtime.GOARCH {
		t.Errorf("empty platform must fall back to the running binary, got %s/%s", f.OS, f.Arch)
	}
}

func TestGatherSkipsMacOSProbesOffDarwin(t *testing.T) {
	fake := runner.NewFake(func(name string, _ []string) ([]byte, error) {
		t.Errorf("no system probe should run on linux, got %s", name)
		return nil, errors.New("unexpected")
	})
	g := Gatherer{OS: "linux", Arch: "amd64", Runner: fake, LookPath: func(string) (string, error) { return "", errors.New("nope") }}
	f := g.Gather(context.Background())
	if f.OS != "linux" || f.MacOSVersion != "" || f.HVSupport {
		t.Errorf("unexpected facts: %+v", f)
	}
}

func TestGatherToleratesFailingCommands(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, errors.New("nope") })
	g := Gatherer{Runner: fake, LookPath: func(string) (string, error) { return "", errors.New("nope") }}
	f := g.Gather(context.Background())
	if f.HVSupport || f.HasBrew || f.HasLima || f.MacOSVersion != "" {
		t.Errorf("failures should yield empty facts, got %+v", f)
	}
}
