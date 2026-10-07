package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/agent"
	"github.com/itlabs-gmbh/cracklet/internal/app"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

func fakeHandler(t *testing.T) runner.FakeHandler {
	t.Helper()
	return func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "limactl list"):
			return []byte(`{"name":"cracklet","status":"Running"}` + "\n"), nil
		case strings.Contains(joined, "sha256sum"):
			return []byte(agent.Checksum() + "\n"), nil
		case strings.Contains(joined, config.AgentPath+" ls"):
			return []byte(`[{"name":"vm1","index":1,"ip":"172.16.1.2","state":"running","vcpus":2,"mem_mib":1024}]`), nil
		case strings.Contains(joined, config.AgentPath+" new "):
			return []byte(`{"name":"box","index":2,"ip":"172.16.2.2","state":"running"}`), nil
		case strings.Contains(joined, config.AgentPath+" rm "):
			return nil, nil
		case name == "ssh", name == "security":
			return nil, nil
		}
		return nil, errors.New("unexpected: " + joined)
	}
}

func run(t *testing.T, args ...string) (string, *runner.Fake, error) {
	t.Helper()
	fake := runner.NewFake(fakeHandler(t))
	buf := &bytes.Buffer{}
	root := newRoot(func(out io.Writer) (*app.App, error) {
		return app.New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, out), nil
	})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), fake, err
}

func TestLsPrintsTable(t *testing.T) {
	out, _, err := run(t, "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "vm1") || !strings.Contains(out, "172.16.1.2") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestNewPassesFlags(t *testing.T) {
	_, fake, err := run(t, "new", "box", "--vcpus", "3", "--mem", "2048", "--disk", "4g")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !fake.Called("limactl shell cracklet -- sudo " + config.AgentPath + " new box 3 2048 4G snapshot base") {
		t.Errorf("flags not forwarded (disk must be canonical):\n%s", fake.Dump())
	}
}

func TestNewDefaultsAndAutoName(t *testing.T) {
	_, fake, err := run(t, "new")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !fake.Called("limactl shell cracklet -- sudo " + config.AgentPath + " new - 2 1024 2G snapshot base") {
		t.Errorf("defaults not applied:\n%s", fake.Dump())
	}
}

func TestRmRequiresName(t *testing.T) {
	if _, _, err := run(t, "rm"); err == nil {
		t.Fatal("expected usage error")
	}
}

func TestRmCallsAgent(t *testing.T) {
	_, fake, err := run(t, "rm", "vm1")
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !fake.Called("limactl shell cracklet -- sudo " + config.AgentPath + " rm vm1") {
		t.Errorf("rm not forwarded:\n%s", fake.Dump())
	}
}

func TestSSHForwardsCommandWithFlags(t *testing.T) {
	_, fake, err := run(t, "ssh", "vm1", "ls", "-la")
	if err != nil {
		t.Fatalf("ssh: %v", err)
	}
	found := false
	for _, c := range fake.Calls() {
		if strings.HasPrefix(c, "ssh -F ") && strings.HasSuffix(c, " vm1.cracklet -- ls -la") {
			found = true
		}
	}
	if !found {
		t.Errorf("ssh command not forwarded verbatim after --:\n%s", fake.Dump())
	}
}

func TestSSHStripsLeadingSeparator(t *testing.T) {
	_, fake, err := run(t, "ssh", "vm1", "--", "echo", "hi")
	if err != nil {
		t.Fatalf("ssh: %v", err)
	}
	if !fake.CalledWithSuffix(" vm1.cracklet -- echo hi") || fake.CalledWithSuffix(" vm1.cracklet -- -- echo hi") {
		t.Errorf("a user-supplied -- must not be doubled:\n%s", fake.Dump())
	}
}

func TestBuilderErrorsSurface(t *testing.T) {
	root := newRoot(func(io.Writer) (*app.App, error) { return nil, errors.New("no home") })
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"ls"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "no home") {
		t.Fatalf("expected builder error, got %v", err)
	}
}

func TestExitCodeMapping(t *testing.T) {
	stderr := &bytes.Buffer{}
	if code := exitCode(nil, stderr); code != 0 {
		t.Errorf("nil error should exit 0, got %d", code)
	}
	if code := exitCode(&app.RemoteExitError{Code: 7}, stderr); code != 7 || stderr.Len() != 0 {
		t.Errorf("remote exit should be silent and mirror the code, got %d / %q", code, stderr.String())
	}
	if code := exitCode(&app.RemoteExitError{Code: 143, Reason: "ssh terminated by signal terminated"}, stderr); code != 143 || !strings.Contains(stderr.String(), "signal") {
		t.Errorf("signal deaths should be explained, got %d / %q", code, stderr.String())
	}
	stderr.Reset()
	if code := exitCode(errors.New("boom"), stderr); code != 1 || !strings.Contains(stderr.String(), "boom") {
		t.Errorf("generic errors should print and exit 1, got %d / %q", code, stderr.String())
	}
}

func TestStartStopForwardToAgent(t *testing.T) {
	handler := func(name string, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, config.AgentPath+" start vm1") || strings.Contains(joined, config.AgentPath+" stop vm1") {
			return []byte(`{"name":"vm1","index":1,"ip":"172.16.1.2","state":"running"}`), nil
		}
		return fakeHandler(t)(name, args)
	}
	fake := runner.NewFake(handler)
	for _, verb := range []string{"start", "stop"} {
		root := newRoot(func(out io.Writer) (*app.App, error) {
			return app.New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, out), nil
		})
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		root.SetArgs([]string{verb, "vm1"})
		if err := root.Execute(); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if !fake.CalledWithSuffix(config.AgentPath + " " + verb + " vm1") {
			t.Errorf("%s not forwarded:\n%s", verb, fake.Dump())
		}
	}
}

func TestPrepareAndDoctorWiring(t *testing.T) {
	handler := func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case name == "sw_vers":
			return []byte("26.7\n"), nil
		case joined == "sysctl -n machdep.cpu.brand_string":
			return []byte("Apple M4 Pro\n"), nil
		case joined == "sysctl -n kern.hv_support":
			return []byte("1\n"), nil
		}
		return fakeHandler(t)(name, args)
	}
	fake := runner.NewFake(handler)
	home := t.TempDir()
	build := func(out io.Writer) (*app.App, error) {
		return app.New(fake, config.Paths{Home: home, LimaHome: "/tmp/lima"}, out,
			app.WithPlatform("darwin", "arm64"),
			app.WithLookPath(func(string) (string, error) { return "/usr/local/bin/x", nil })), nil
	}
	out := &bytes.Buffer{}
	root := newRoot(build)
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"doctor"})
	if err := root.Execute(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(out.String(), "Running") {
		t.Errorf("doctor output should report the instance state:\n%s", out.String())
	}

	root = newRoot(build)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"prepare", "--cpus", "0"})
	if err := root.Execute(); err == nil {
		t.Fatal("prepare must reject --cpus 0")
	}
}

func TestDefaultBuilder(t *testing.T) {
	t.Setenv("CRACKLET_HOME", t.TempDir())
	if a, err := defaultBuilder(io.Discard); err != nil || a == nil {
		t.Fatalf("defaultBuilder: %v", err)
	}
}

func TestForwardCommands(t *testing.T) {
	handler := func(name string, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, config.AgentPath+" forward web"):
			return []byte(`{"name":"web","index":1,"ip":"172.16.1.2","state":"running","forwards":[{"host":8080,"guest":80,"state":"active"}]}`), nil
		case strings.Contains(joined, config.AgentPath+" unforward web"):
			return []byte(`{"name":"web","index":1,"ip":"172.16.1.2","state":"running","forwards":[]}`), nil
		case strings.Contains(joined, config.AgentPath+" ls"):
			return []byte(`[{"name":"web","index":1,"ip":"172.16.1.2","state":"running","vcpus":2,"mem_mib":1024,"forwards":[{"host":8080,"guest":80,"state":"active"},{"host":3000,"guest":3000,"state":"active"}]}]`), nil
		}
		return fakeHandler(t)(name, args)
	}
	fake := runner.NewFake(handler)
	exec := func(args ...string) (string, error) {
		buf := &bytes.Buffer{}
		root := newRoot(func(out io.Writer) (*app.App, error) {
			return app.New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, out,
				app.WithPortProbe(func(context.Context, int) bool { return true }),
				app.WithPortCheck(func(int) bool { return false })), nil
		})
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs(args)
		err := root.Execute()
		return buf.String(), err
	}

	if out, err := exec("forward", "web", "8080:80"); err != nil || !strings.Contains(out, "ready") {
		t.Errorf("forward: %v %q", err, out)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " forward web 8080:80") {
		t.Errorf("forward not forwarded:\n%s", fake.Dump())
	}
	if out, err := exec("forward", "web"); err != nil || !strings.Contains(out, "localhost:8080") || !strings.Contains(out, "web:80") {
		t.Errorf("forward listing: %v %q", err, out)
	}
	if _, err := exec("unforward", "web", "8080"); err != nil {
		t.Errorf("unforward: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " unforward web 8080") {
		t.Errorf("unforward not forwarded:\n%s", fake.Dump())
	}
	if _, err := exec("unforward", "web", "abc"); err == nil {
		t.Error("non-numeric port must be rejected")
	}
	if out, err := exec("ls"); err != nil || !strings.Contains(out, "PORTS") || !strings.Contains(out, "8080→80,3000→3000") {
		t.Errorf("ls should show forwards: %v %q", err, out)
	}
}

func TestNewWithPortFlags(t *testing.T) {
	handler := func(name string, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, config.AgentPath+" new "):
			return []byte(`{"name":"web","index":1,"ip":"172.16.1.2","state":"running","forwards":[]}`), nil
		case strings.Contains(joined, config.AgentPath+" forward web"):
			return []byte(`{"name":"web","index":1,"ip":"172.16.1.2","state":"running","forwards":[{"host":8080,"guest":80,"state":"active"},{"host":3000,"guest":3000,"state":"active"}]}`), nil
		}
		return fakeHandler(t)(name, args)
	}
	fake := runner.NewFake(handler)
	root := newRoot(func(out io.Writer) (*app.App, error) {
		return app.New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, out,
			app.WithPortProbe(func(context.Context, int) bool { return true }),
			app.WithPortCheck(func(int) bool { return false })), nil
	})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"new", "web", "-p", "8080:80", "-p", "3000"})
	if err := root.Execute(); err != nil {
		t.Fatalf("new: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " forward web 8080:80 3000:3000") {
		t.Errorf("-p flags not applied:\n%s", fake.Dump())
	}
}

func TestNewFreshFlag(t *testing.T) {
	_, fake, err := run(t, "new", "box", "--fresh")
	if err != nil {
		t.Fatalf("new --fresh: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " new box 2 1024 2G fresh base") {
		t.Errorf("--fresh not forwarded:\n%s", fake.Dump())
	}
}

func TestTunnelWiring(t *testing.T) {
	if _, _, err := run(t, "tunnel"); err == nil {
		t.Error("tunnel without a name must fail")
	}
	_, fake, err := run(t, "tunnel", "vm1")
	if err == nil || !strings.Contains(err.Error(), "cracklet grant vm1") {
		t.Errorf("tunnel without broker grants should hint at grant, got %v", err)
	}
	for _, c := range fake.Calls() {
		if strings.HasPrefix(c, "ssh ") {
			t.Errorf("no ssh without grants: %s", c)
		}
	}
}

func TestNewProfileDefaultsDiskToImageSize(t *testing.T) {
	_, fake, err := run(t, "new", "agent", "--profile", "paseo")
	if err != nil {
		t.Fatalf("new --profile: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " new agent 2 1024 8G snapshot paseo") {
		t.Errorf("profile and its image size not forwarded:\n%s", fake.Dump())
	}
}

func TestNewRejectsUnknownProfile(t *testing.T) {
	_, fake, err := run(t, "new", "--profile", "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("expected unknown profile error, got %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("an invalid profile must be rejected before the agent runs:\n%s", fake.Dump())
	}
}
