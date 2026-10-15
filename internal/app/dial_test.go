package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/runner"
)

// dialBackend is a Backend that only knows how to Dial: every connection goes
// to the test's TCP listener, and it records what it was asked for.
type dialBackend struct {
	backend.Backend // nil: any other method panics, which a Dial test must not need
	addr            string
	name            string
	port            int
	err             error
}

func (d *dialBackend) Dial(ctx context.Context, name string, port int) (backend.Conn, error) {
	d.name, d.port = name, port
	if d.err != nil {
		return nil, d.err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, err
	}
	return conn.(*net.TCPConn), nil
}

// echoUpper serves one connection: it upper-cases what it reads, then closes.
func echoUpper(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn) // until the client half-closes
		_, _ = conn.Write(bytes.ToUpper(data))
	}()
	return ln.Addr().String()
}

func dialApp(t *testing.T, d *dialBackend) *App {
	t.Helper()
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		return nil, errors.New("no commands expected")
	})
	return New(fake, config.Paths{Home: t.TempDir()}, &bytes.Buffer{}, WithBackend(d))
}

func TestDialRelaysBothWaysAndStripsAlias(t *testing.T) {
	d := &dialBackend{addr: echoUpper(t)}
	a := dialApp(t, d)
	var out bytes.Buffer
	if err := a.Dial(context.Background(), "vm1.skiff", 22, strings.NewReader("hello guest"), &out); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if out.String() != "HELLO GUEST" {
		t.Errorf("relayed %q", out.String())
	}
	if d.name != "vm1" || d.port != 22 {
		t.Errorf("backend asked for %s:%d, want vm1:22", d.name, d.port)
	}
}

func TestDialRejectsBadInputBeforeConnecting(t *testing.T) {
	d := &dialBackend{addr: "127.0.0.1:1"}
	a := dialApp(t, d)
	for _, tc := range []struct {
		host string
		port int
	}{{"../etc", 22}, {"vm1", 0}, {"vm1", 70000}, {"", 22}} {
		if err := a.Dial(context.Background(), tc.host, tc.port, strings.NewReader(""), io.Discard); err == nil {
			t.Errorf("Dial(%q, %d) must fail", tc.host, tc.port)
		}
	}
	if d.name != "" {
		t.Errorf("backend must not be asked for invalid input, got %q", d.name)
	}
}

func TestDialSurfacesBackendError(t *testing.T) {
	d := &dialBackend{err: backend.ErrNoDial}
	a := dialApp(t, d)
	err := a.Dial(context.Background(), "vm1", 22, strings.NewReader(""), io.Discard)
	if !errors.Is(err, backend.ErrNoDial) || !strings.Contains(err.Error(), "vm1:22") {
		t.Errorf("want wrapped ErrNoDial naming the target, got %v", err)
	}
}

func TestDialStopsWhenContextEnds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			<-time.After(5 * time.Second) // a guest that never answers
			conn.Close()
		}
	}()
	a := dialApp(t, &dialBackend{addr: ln.Addr().String()})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	stdin, _ := io.Pipe() // stays silent until Dial returns
	defer stdin.Close()
	err = a.Dial(ctx, "vm1", 22, stdin, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want DeadlineExceeded, got %v", err)
	}
}

// pinningBackend is a dialBackend that pins host keys, as the container
// backend does.
type pinningBackend struct {
	*dialBackend
	knownHosts string
}

func (p *pinningBackend) Name() string            { return "pinning" }
func (p *pinningBackend) SSHProxyCommand() string { return "/usr/local/bin/skiff _dial %h %p" }
func (p *pinningBackend) KnownHostsFile() string  { return p.knownHosts }

func TestSSHConfigPinsHostKeysOfAPinningBackend(t *testing.T) {
	a := dialApp(t, nil)
	a.backend = &pinningBackend{dialBackend: &dialBackend{}, knownHosts: "/home/known_hosts"}
	if err := a.writeSSHConfig(); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(a.paths.SSHConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"StrictHostKeyChecking yes", "UserKnownHostsFile /home/known_hosts", "ProxyCommand /usr/local/bin/skiff _dial %h %p"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// relayBackend serves forwards itself, as the container backend does.
type relayBackend struct {
	*dialBackend
	served bool
}

func (r *relayBackend) ServeForwards(ctx context.Context) error {
	r.served = true
	return nil
}

func TestRelayRunsTheBackendsRelayOrRefuses(t *testing.T) {
	r := &relayBackend{dialBackend: &dialBackend{}}
	a := dialApp(t, nil)
	a.backend = r
	if err := a.Relay(context.Background()); err != nil || !r.served {
		t.Errorf("Relay = %v, served=%v", err, r.served)
	}
	a.backend = &pinningBackend{dialBackend: &dialBackend{}}
	if err := a.Relay(context.Background()); err == nil || !strings.Contains(err.Error(), "relay") {
		t.Errorf("a backend without a relay must be refused clearly, got %v", err)
	}
}

func TestSSHConfigIsRewrittenWhenTheBackendRendersSomethingElse(t *testing.T) {
	a := dialApp(t, nil)
	a.backend = &pinningBackend{dialBackend: &dialBackend{}, knownHosts: ""}
	if err := a.ensureSSHConfig(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(a.paths.SSHConfigPath())
	if !strings.Contains(string(before), "StrictHostKeyChecking no") {
		t.Fatalf("unexpected config:\n%s", before)
	}
	// An upgrade pins host keys: the next session must not keep the old file.
	a.backend = &pinningBackend{dialBackend: &dialBackend{}, knownHosts: "/home/known_hosts"}
	if err := a.ensureSSHConfig(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(a.paths.SSHConfigPath())
	if !strings.Contains(string(after), "StrictHostKeyChecking yes") {
		t.Errorf("config not upgraded:\n%s", after)
	}
	stat, _ := os.Stat(a.paths.SSHConfigPath())
	if err := a.ensureSSHConfig(); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.Stat(a.paths.SSHConfigPath()); !again.ModTime().Equal(stat.ModTime()) {
		t.Error("an up-to-date config must not be rewritten")
	}
}
