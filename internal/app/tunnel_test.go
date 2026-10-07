package app

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/grant"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// tunnelScript scripts the VM states `ls` reports and what each `ssh -N`
// attempt returns. onSSH runs inside the fake ssh call.
type tunnelScript struct {
	mu     sync.Mutex
	states []string
	sshErr []error
	onSSH  func(attempt int)
	ssh    int
}

func (s *tunnelScript) handle(name string, args []string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "ssh" && len(args) > 0 && args[0] == "-N" {
		s.ssh++
		if s.onSSH != nil {
			s.onSSH(s.ssh)
		}
		if s.ssh <= len(s.sshErr) {
			return nil, s.sshErr[s.ssh-1]
		}
		return nil, nil
	}
	if name == "ssh" {
		return nil, nil // an interactive session
	}
	if name == "limactl" && args[len(args)-1] == "ls" {
		state := s.states[0]
		if state == "error" {
			s.states = s.states[1:]
			return nil, errors.New("limactl: connection refused")
		}
		if len(s.states) > 1 {
			s.states = s.states[1:]
		}
		return []byte(`[{"name":"agent1","state":"` + state + `"}]`), nil
	}
	return defaultHandler(nil)(name, args)
}

func newTunnelApp(t *testing.T, script *tunnelScript, grants ...string) (*App, *runner.Fake) {
	t.Helper()
	fake := runner.NewFake(script.handle)
	app := New(fake, testPaths(t), &strings.Builder{}, WithEnvd(fakeEnvd), WithTunnel(fakeTunnel))
	app.tunnelRetry = time.Millisecond
	set, err := grant.ParseSet(grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.grantStore().Save("agent1", set); err != nil {
		t.Fatal(err)
	}
	return app, fake
}

func tunnelCalls(fake *runner.Fake) []string {
	var calls []string
	for _, c := range fake.Calls() {
		if strings.HasPrefix(c, "ssh -N ") {
			calls = append(calls, c)
		}
	}
	return calls
}

func TestTunnelHoldsForwardUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	script := &tunnelScript{states: []string{"running"}, onSSH: func(int) { cancel() }}
	app, fake := newTunnelApp(t, script, "claude")
	if err := app.Tunnel(ctx, "agent1"); err != nil {
		t.Fatalf("Tunnel: %v", err)
	}
	calls := tunnelCalls(fake)
	if len(calls) != 1 {
		t.Fatalf("want one ssh -N call, got:\n%s", fake.Dump())
	}
	for _, want := range []string{
		"-n ", "-o ExitOnForwardFailure=yes", "-o ServerAliveInterval=15", "-o ServerAliveCountMax=3",
		"-R 127.0.0.1:7777:/tmp/fake-agent1.sock",
	} {
		if !strings.Contains(calls[0], want) {
			t.Errorf("missing %q in %s", want, calls[0])
		}
	}
	if !strings.HasSuffix(calls[0], "agent1.cracklet") || strings.Contains(calls[0], " -A ") {
		t.Errorf("tunnel must run no command and forward no agent: %s", calls[0])
	}
	audit, err := os.ReadFile(app.paths.AuditLog())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "vm=agent1 tunnel=open") || !strings.Contains(string(audit), "vm=agent1 tunnel=closed") {
		t.Errorf("audit log should record the tunnel:\n%s", audit)
	}
}

func TestTunnelReconnectsUntilVMStops(t *testing.T) {
	dropped := errors.New("exit status 255")
	script := &tunnelScript{
		states: []string{"running", "running", "stopped"},
		sshErr: []error{dropped, dropped},
	}
	app, fake := newTunnelApp(t, script, "github:org/repo")
	if err := app.Tunnel(context.Background(), "agent1"); err != nil {
		t.Fatalf("Tunnel: %v", err)
	}
	if n := len(tunnelCalls(fake)); n != 2 {
		t.Errorf("want 2 attempts before the VM stopped, got %d:\n%s", n, fake.Dump())
	}
	audit, err := os.ReadFile(app.paths.AuditLog())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), `tunnel=closed reason="exit status 255"`) {
		t.Errorf("audit log should say why the tunnel closed:\n%s", audit)
	}
}

func TestTunnelRequiresRunningVM(t *testing.T) {
	app, fake := newTunnelApp(t, &tunnelScript{states: []string{"stopped"}}, "claude")
	err := app.Tunnel(context.Background(), "agent1")
	if err == nil || !strings.Contains(err.Error(), "cracklet start agent1") {
		t.Errorf("want a start hint, got %v", err)
	}
	if len(tunnelCalls(fake)) != 0 {
		t.Errorf("no ssh for a stopped VM:\n%s", fake.Dump())
	}
}

func TestTunnelNeedsBrokerGrant(t *testing.T) {
	for _, grants := range [][]string{nil, {"ssh-agent"}} {
		app, _ := newTunnelApp(t, &tunnelScript{states: []string{"running"}}, grants...)
		err := app.Tunnel(context.Background(), "agent1")
		if err == nil || !strings.Contains(err.Error(), "cracklet grant agent1") {
			t.Errorf("grants %v: want a grant hint, got %v", grants, err)
		}
	}
}

// newSharedBrokerApp returns an App with a short home (Unix socket limits on
// macOS), a claude grant for agent1, and a stand-in broker already answering
// on agent1's socket. closeBroker makes that broker go away.
func newSharedBrokerApp(t *testing.T, handle runner.FakeHandler) (app *App, fake *runner.Fake, closeBroker func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cracklet-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	fake = runner.NewFake(handle)
	app = New(fake, config.Paths{Home: dir, LimaHome: "/tmp/lima"}, &strings.Builder{}, WithEnvd(fakeEnvd), WithTunnel(fakeTunnel))
	app.tunnelRetry = time.Millisecond
	app.brokerWatch = time.Millisecond
	set, _ := grant.ParseSet([]string{"claude"})
	if err := app.grantStore().Save("agent1", set); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(app.paths.RunDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", app.paths.SocketPath("agent1"))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	var once sync.Once
	closeBroker = func() { once.Do(func() { _ = ln.Close() }) }
	t.Cleanup(closeBroker)
	return app, fake, closeBroker
}

func TestSSHSkipsForwardWhenBrokerAlreadyServes(t *testing.T) {
	script := &tunnelScript{states: []string{"running"}}
	app, fake, _ := newSharedBrokerApp(t, script.handle)
	if err := app.SSH(context.Background(), "agent1", nil); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "-R ") {
			t.Errorf("the session owning the broker already holds the guest port: %s", c)
		}
	}
}

func TestSSHTakesOverBrokerWhenOwnerLeaves(t *testing.T) {
	script := &tunnelScript{states: []string{"running"}}
	var (
		closeBroker func()
		fake        *runner.Fake
		took        = make(chan string, 1)
	)
	handle := func(name string, args []string) ([]byte, error) {
		if name == "ssh" && len(args) > 0 && args[0] != "-N" {
			// The interactive session: its peer ends, the takeover must follow.
			closeBroker()
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				for _, c := range fake.Calls() {
					if strings.HasPrefix(c, "ssh -N ") {
						took <- c
						return nil, nil
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
			return nil, nil
		}
		return script.handle(name, args)
	}
	var app *App
	app, fake, closeBroker = newSharedBrokerApp(t, handle)
	if err := app.SSH(context.Background(), "agent1", nil); err != nil {
		t.Fatalf("SSH: %v", err)
	}
	select {
	case c := <-took:
		if !strings.Contains(c, "-R 127.0.0.1:7777:/tmp/fake-agent1.sock") {
			t.Errorf("takeover must forward the broker: %s", c)
		}
	default:
		t.Fatalf("no takeover after the broker owner left:\n%s", fake.Dump())
	}
}

func TestTunnelSurvivesTransientListErrors(t *testing.T) {
	dropped := errors.New("exit status 255")
	script := &tunnelScript{
		states: []string{"running", "error", "stopped"},
		sshErr: []error{dropped, dropped},
	}
	app, fake := newTunnelApp(t, script, "claude")
	if err := app.Tunnel(context.Background(), "agent1"); err != nil {
		t.Fatalf("one failed status check must not end the tunnel: %v", err)
	}
	if n := len(tunnelCalls(fake)); n != 2 {
		t.Errorf("want 2 attempts, got %d:\n%s", n, fake.Dump())
	}
}

func TestTunnelGivesUpAfterRepeatedListErrors(t *testing.T) {
	states := []string{"running"}
	for i := 0; i < maxTunnelFailures; i++ {
		states = append(states, "error")
	}
	script := &tunnelScript{states: append(states, "running"), sshErr: make([]error, maxTunnelFailures+1)}
	for i := range script.sshErr {
		script.sshErr[i] = errors.New("exit status 255")
	}
	app, _ := newTunnelApp(t, script, "claude")
	err := app.Tunnel(context.Background(), "agent1")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("want the list error after %d failures, got %v", maxTunnelFailures, err)
	}
}

func TestNextTunnelRetryBacksOff(t *testing.T) {
	app := &App{tunnelRetry: time.Second}
	cases := []struct {
		prev, lasted, want time.Duration
	}{
		{time.Second, 0, 2 * time.Second},
		{40 * time.Second, 0, maxTunnelRetry},
		{maxTunnelRetry, 0, maxTunnelRetry},
		{maxTunnelRetry, tunnelStable, time.Second},
	}
	for _, c := range cases {
		if got := app.nextTunnelRetry(c.prev, c.lasted); got != c.want {
			t.Errorf("nextTunnelRetry(%s, %s) = %s, want %s", c.prev, c.lasted, got, c.want)
		}
	}
}
