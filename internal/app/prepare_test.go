package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/agent"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/envdbin"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// hostHandler answers the macOS probes and records everything else.
func hostHandler(limaStatus string, agentChecksum string) runner.FakeHandler {
	return func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case name == "sw_vers":
			return []byte("26.7\n"), nil
		case joined == "sysctl -n machdep.cpu.brand_string":
			return []byte("Apple M4 Pro\n"), nil
		case joined == "sysctl -n kern.hv_support":
			return []byte("1\n"), nil
		case strings.HasPrefix(joined, "limactl list"):
			if limaStatus == "" {
				return []byte(""), nil
			}
			return []byte(`{"name":"cracklet","status":"` + limaStatus + `"}` + "\n"), nil
		case strings.Contains(joined, "sha256sum"):
			return []byte(agentChecksum + "\n"), nil
		}
		return nil, nil
	}
}

func writeDummyKeys(t *testing.T, paths config.Paths) {
	t.Helper()
	if err := os.MkdirAll(paths.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{paths.KeyPath(), paths.PubKeyPath()} {
		if err := os.WriteFile(p, []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func prepareApp(t *testing.T, handler runner.FakeHandler, hasLima bool, opts ...Option) (*App, *runner.Fake, *bytes.Buffer, config.Paths) {
	t.Helper()
	fake := runner.NewFake(handler)
	out := &bytes.Buffer{}
	paths := config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}
	lookPath := func(name string) (string, error) {
		if name == "limactl" && !hasLima {
			return "", errors.New("missing")
		}
		return "/usr/local/bin/" + name, nil
	}
	// Pin the platform so the suite behaves identically on Linux CI runners
	// and on the Apple Silicon Macs cracklet actually targets.
	opts = append([]Option{WithPlatform("darwin", "arm64"), WithLookPath(lookPath), WithEnvd(fakeEnvd)}, opts...)
	return New(fake, paths, out, opts...), fake, out, paths
}

func TestPrepareCreatesInstanceAndImages(t *testing.T) {
	app, fake, out, paths := prepareApp(t, hostHandler("", agent.Checksum()), true)
	writeDummyKeys(t, paths)

	err := app.Prepare(context.Background(), PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	for _, want := range []string{
		"limactl start --tty=false --name cracklet " + paths.LimaTemplatePath(),
		"limactl shell cracklet -- sudo " + config.AgentPath + " prepare " +
			config.FirecrackerVersion + " " + config.FirecrackerSHA256 + " " +
			config.KernelURL + " " + config.KernelSHA256 + " " +
			config.RootfsURL + " " + config.RootfsSHA256 + " 2 1024",
	} {
		if !fake.Called(want) {
			t.Errorf("missing call %q in:\n%s", want, fake.Dump())
		}
	}
	if in, ok := fake.Input("sh " + config.GuestKeyPath + " 0600"); !ok || in != "key" {
		t.Errorf("private key must be streamed with mode 0600, got %q ok=%v", in, ok)
	}
	if _, ok := fake.Input("sh " + config.GuestKeyPath + ".pub 0644"); !ok {
		t.Errorf("public key must be streamed with mode 0644:\n%s", fake.Dump())
	}
	if in, ok := fake.Input("sh " + config.GuestEnvdPath + " 0755"); !ok || len(in) < 1024 {
		t.Errorf("cracklet-envd binary must be streamed with mode 0755 (got %d bytes, ok=%v)", len(in), ok)
	}
	if fake.Called("brew install lima") {
		t.Error("lima is present, brew must not run")
	}
	if _, err := os.Stat(paths.SSHConfigPath()); err != nil {
		t.Errorf("ssh config should be written: %v", err)
	}
	if tmpl, _ := os.ReadFile(paths.LimaTemplatePath()); !strings.Contains(string(tmpl), "nestedVirtualization: true") {
		t.Error("lima template should enable nested virtualization")
	}
	if !strings.Contains(out.String(), "Include "+paths.SSHConfigPath()) {
		t.Errorf("output should explain the ssh Include, got:\n%s", out.String())
	}
}

func TestPrepareBuildsRequestedProfiles(t *testing.T) {
	app, fake, _, paths := prepareApp(t, hostHandler("Running", agent.Checksum()), true)
	writeDummyKeys(t, paths)
	o := PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40, Profiles: []string{"paseo", "base", "paseo"}}
	if err := app.Prepare(context.Background(), o); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !fake.CalledWithSuffix(config.RootfsSHA256 + " 2 1024 paseo") {
		t.Errorf("extra profiles must be passed once, base implicitly:\n%s", fake.Dump())
	}
}

func TestPrepareRejectsUnknownProfileBeforeSideEffects(t *testing.T) {
	app, fake, _, _ := prepareApp(t, hostHandler("", agent.Checksum()), true)
	o := PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40, Profiles: []string{"nope"}}
	if err := app.Prepare(context.Background(), o); err == nil {
		t.Fatal("expected profile error")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("invalid profiles must be rejected before anything runs, got:\n%s", fake.Dump())
	}
}

func TestPrepareStartsStoppedInstanceAndInstallsLima(t *testing.T) {
	app, fake, _, paths := prepareApp(t, hostHandler("Stopped", agent.Checksum()), false)
	writeDummyKeys(t, paths)
	if err := app.Prepare(context.Background(), PrepareOptions{CPUs: 2, MemoryGiB: 4, DiskGiB: 20}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !fake.Called("brew install lima") {
		t.Errorf("expected brew install, got:\n%s", fake.Dump())
	}
	if !fake.Called("limactl start --tty=false cracklet") {
		t.Errorf("expected instance start, got:\n%s", fake.Dump())
	}
}

func TestPrepareGeneratesKeyWhenMissing(t *testing.T) {
	var paths config.Paths
	app, fake, _, p := prepareApp(t, func(name string, args []string) ([]byte, error) {
		if name == "ssh-keygen" {
			writeDummyKeys(t, paths)
			return nil, nil
		}
		return hostHandler("Running", agent.Checksum())(name, args)
	}, true)
	paths = p
	if err := app.Prepare(context.Background(), PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !fake.Called("ssh-keygen -q -t ed25519 -N  -C cracklet -f " + paths.KeyPath()) {
		t.Errorf("expected ssh-keygen, got:\n%s", fake.Dump())
	}
}

func TestPrepareStopsOnFatalPreflight(t *testing.T) {
	app, fake, out, _ := prepareApp(t, func(name string, args []string) ([]byte, error) {
		if name == "sw_vers" {
			return []byte("14.0\n"), nil
		}
		return hostHandler("", agent.Checksum())(name, args)
	}, true)
	err := app.Prepare(context.Background(), PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40})
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if !strings.Contains(out.String(), "[ERROR]") {
		t.Errorf("problems should be printed, got:\n%s", out.String())
	}
	for _, c := range fake.Calls() {
		if strings.HasPrefix(c, "limactl start") {
			t.Errorf("must not create instance after fatal preflight: %s", c)
		}
	}
}

func TestPrepareRejectsBadSizingBeforeSideEffects(t *testing.T) {
	app, fake, _, _ := prepareApp(t, hostHandler("", agent.Checksum()), true)
	if err := app.Prepare(context.Background(), PrepareOptions{CPUs: 0, MemoryGiB: 8, DiskGiB: 40}); err == nil {
		t.Fatal("expected sizing error")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("invalid flags must be rejected before anything runs, got:\n%s", fake.Dump())
	}
}

func TestDoctorWithoutLimaDoesNotFail(t *testing.T) {
	app, fake, out, _ := prepareApp(t, hostHandler("", agent.Checksum()), false)
	if err := app.Doctor(context.Background()); err != nil {
		t.Fatalf("Doctor must succeed when Lima is merely missing: %v", err)
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Errorf("expected a hint that Lima is not installed, got:\n%s", out.String())
	}
	if fake.CalledWithSuffix("limactl list --format json") {
		t.Error("limactl must not be invoked when it is not installed")
	}
}

func TestDoctorReportsInstanceState(t *testing.T) {
	app, _, out, _ := prepareApp(t, hostHandler("Running", agent.Checksum()), true)
	if err := app.Doctor(context.Background()); err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !strings.Contains(out.String(), "Apple M4 Pro") || !strings.Contains(out.String(), "Running") {
		t.Errorf("unexpected doctor output:\n%s", out.String())
	}
}

func TestDoctorRejectsUnsupportedPlatform(t *testing.T) {
	app, fake, out, _ := prepareApp(t, hostHandler("Running", agent.Checksum()), true, WithPlatform("linux", "amd64"))
	err := app.Doctor(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("Doctor must fail on linux/amd64, got %v", err)
	}
	if !strings.Contains(out.String(), "linux/amd64") {
		t.Errorf("output should name the offending platform, got:\n%s", out.String())
	}
	if fake.Called("sw_vers -productVersion") {
		t.Errorf("macOS probes must not run on a non-darwin platform:\n%s", fake.Dump())
	}
}

func TestDoctorFailsOnFatalProblem(t *testing.T) {
	app, _, _, _ := prepareApp(t, func(name string, args []string) ([]byte, error) {
		if strings.Join(args, " ") == "-n machdep.cpu.brand_string" {
			return []byte("Apple M1\n"), nil
		}
		return hostHandler("", agent.Checksum())(name, args)
	}, true)
	if err := app.Doctor(context.Background()); err == nil {
		t.Fatal("expected error for M1")
	}
}

func TestSSHWritesConfigAndRunsSSH(t *testing.T) {
	app, fake, _, paths := prepareApp(t, hostHandler("Running", agent.Checksum()), true)
	if err := app.SSH(context.Background(), "vm1", []string{"true"}); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	if !fake.Called("ssh -F " + paths.SSHConfigPath() + " vm1.cracklet -- true") {
		t.Errorf("expected ssh call, got:\n%s", fake.Dump())
	}
	if _, err := os.Stat(filepath.Join(paths.Home, "ssh_config")); err != nil {
		t.Errorf("ssh config should exist: %v", err)
	}
}

func TestLifecycleCommands(t *testing.T) {
	app, fake, _, _ := prepareApp(t, func(name string, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, config.AgentPath+" start") || strings.Contains(joined, config.AgentPath+" stop") {
			return []byte(`{"name":"vm1","index":1,"ip":"172.16.1.2","state":"running"}`), nil
		}
		return hostHandler("Running", agent.Checksum())(name, args)
	}, true)
	if _, err := app.StartVM(context.Background(), "vm1"); err != nil {
		t.Fatalf("StartVM: %v", err)
	}
	if _, err := app.StopVM(context.Background(), "vm1"); err != nil {
		t.Fatalf("StopVM: %v", err)
	}
	for _, want := range []string{
		"limactl shell cracklet -- sudo " + config.AgentPath + " start vm1",
		"limactl shell cracklet -- sudo " + config.AgentPath + " stop vm1",
	} {
		if !fake.Called(want) {
			t.Errorf("missing %q in:\n%s", want, fake.Dump())
		}
	}
	if _, err := app.StartVM(context.Background(), "Bad"); err == nil {
		t.Error("expected name validation error")
	}
}

func TestSSHExplainsConnectionFailures(t *testing.T) {
	transportErr := exec.Command("sh", "-c", "exit 255").Run()
	app, _, _, _ := prepareApp(t, func(name string, args []string) ([]byte, error) {
		if name == "ssh" {
			return nil, fmt.Errorf("ssh: %w", transportErr)
		}
		return hostHandler("Running", agent.Checksum())(name, args)
	}, true)
	err := app.SSH(context.Background(), "vm1", nil)
	if err == nil || !strings.Contains(err.Error(), "cracklet ls") {
		t.Fatalf("expected a hint pointing at 'cracklet ls', got %v", err)
	}
}

func TestSSHPassesRemoteExitCodeThrough(t *testing.T) {
	remoteErr := exec.Command("sh", "-c", "exit 3").Run()
	app, _, _, _ := prepareApp(t, func(name string, args []string) ([]byte, error) {
		if name == "ssh" {
			return nil, remoteErr
		}
		return hostHandler("Running", agent.Checksum())(name, args)
	}, true)
	err := app.SSH(context.Background(), "vm1", []string{"false"})
	var remote *RemoteExitError
	if !errors.As(err, &remote) || remote.Code != 3 || remote.Reason != "" {
		t.Fatalf("expected silent RemoteExitError with code 3, got %v", err)
	}
}

func TestSSHReportsSignalDeath(t *testing.T) {
	cmd := exec.Command("sh", "-c", "kill -TERM $$")
	signalErr := cmd.Run()
	app, _, _, _ := prepareApp(t, func(name string, args []string) ([]byte, error) {
		if name == "ssh" {
			return nil, signalErr
		}
		return hostHandler("Running", agent.Checksum())(name, args)
	}, true)
	err := app.SSH(context.Background(), "vm1", nil)
	var remote *RemoteExitError
	if !errors.As(err, &remote) || remote.Code != 128+15 || !strings.Contains(remote.Reason, "signal") {
		t.Fatalf("expected 128+SIGTERM with a reason, got %v", err)
	}
}

func TestPrepareFailsWhenGuestDaemonUnavailable(t *testing.T) {
	app, fake, _, paths := prepareApp(t, hostHandler("Running", agent.Checksum()), true,
		WithEnvd(func(context.Context) ([]byte, error) { return nil, errors.New("no toolchain") }))
	writeDummyKeys(t, paths)

	err := app.Prepare(context.Background(), PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40})
	if err == nil || !strings.Contains(err.Error(), "no toolchain") {
		t.Fatalf("Prepare must surface the daemon error, got %v", err)
	}
	if fake.CalledWithSuffix(config.AgentPath + " prepare") {
		t.Error("agent prepare must not run without the daemon")
	}
}

func TestDefaultEnvdSourceBuildsWhenNotEmbedded(t *testing.T) {
	if _, ok := envdbin.Embedded(); ok {
		t.Skip("daemon is embedded in this test binary")
	}
	app, _, out := newTestApp(t, defaultHandler(nil))
	_, err := app.loadEnvd(context.Background())
	if err == nil {
		t.Fatal("fake runner cannot build, want an error")
	}
	if !strings.Contains(out.String(), "cross-compiling") {
		t.Errorf("user must be told the daemon is being built, got %q", out.String())
	}
}
