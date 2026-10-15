package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
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
	if err := a.Dial(context.Background(), "vm1.cracklet", 22, strings.NewReader("hello guest"), &out); err != nil {
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
