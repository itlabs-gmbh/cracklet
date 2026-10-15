package container

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// upperServer answers every connection with the upper-cased line it reads.
func upperServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				_, _ = conn.Write(bytes.ToUpper([]byte(line)))
			}()
		}
	}()
	return ln.Addr().String()
}

// lockedBuffer is a bytes.Buffer the relay goroutines and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// newRelayBackend is newTestBackend with a status writer safe for the relay.
func newRelayBackend(t *testing.T, f *fakeContainer, poll, idle time.Duration) (*Backend, *lockedBuffer) {
	t.Helper()
	out := &lockedBuffer{}
	b, _, _ := newTestBackend(t, f, WithRelayTiming(poll, idle), withStatus(out))
	return b, out
}

// listens reports whether 127.0.0.1:port accepts a connection.
func listens(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// freePort finds a loopback port nothing listens on right now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// roundTrip sends a line through 127.0.0.1:port and returns the answer.
func roundTrip(port int, line string) (string, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, line); err != nil {
		return "", err
	}
	answer, err := io.ReadAll(conn)
	return string(answer), err
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRelayServesRecordedForwardsAndFollowsChanges(t *testing.T) {
	f := newFake()
	b, out := newRelayBackend(t, f, 5*time.Millisecond, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	createDev(t, b)
	f.guestServers["192.168.64.2:80"] = upperServer(t)
	port := freePort(t)
	if _, err := b.Forward(ctx, "dev", []vm.Forward{{Host: port, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.ServeForwards(ctx) }()
	waitFor(t, "the listener", func() bool { return listens(port) })
	if got, err := roundTrip(port, "hello\n"); err != nil || got != "HELLO\n" {
		t.Fatalf("roundTrip = %q, %v", got, err)
	}
	// Stopped guest: the port stays, connections are closed without an
	// answer (or reset, when the request was still unread). The first one
	// may still try the cached address (and forget it); the next one learns
	// that the VM is stopped.
	if _, err := b.Stop(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the stopped VM to be noticed", func() bool {
		got, err := roundTrip(port, "hello\n")
		if got != "" || (err != nil && !strings.Contains(err.Error(), "reset")) {
			t.Fatalf("stopped VM: got %q, %v; want no answer", got, err)
		}
		return strings.Contains(out.String(), "VM 'dev' is stopped")
	})
	// Started again under a new address: the relay follows it.
	if _, err := b.Start(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	f.guestServers["192.168.64.3:80"] = f.guestServers["192.168.64.2:80"]
	// The relay remembered the stopped VM for a moment; it learns the new
	// address within relayNegTTL.
	waitFor(t, "the restarted VM to be served", func() bool {
		got, err := roundTrip(port, "again\n")
		return err == nil && got == "AGAIN\n"
	})
	if _, err := b.Unforward(ctx, "dev", []int{port}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the listener to close", func() bool { return !listens(port) })
	cancel()
	if err := <-done; err != nil {
		t.Errorf("ServeForwards = %v", err)
	}
}

func TestRelayExitsWhenIdle(t *testing.T) {
	b, _, _ := newTestBackend(t, newFake(), WithRelayTiming(5*time.Millisecond, 20*time.Millisecond))
	start := time.Now()
	if err := b.ServeForwards(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("an idle relay must exit after relayIdle")
	}
}

func TestRelayReportsABusyPortOnce(t *testing.T) {
	f := newFake()
	b, out := newRelayBackend(t, f, 2*time.Millisecond, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	createDev(t, b)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if _, err := b.Forward(ctx, "dev", []vm.Forward{{Host: port, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.ServeForwards(ctx) }()
	waitFor(t, "the bind failure", func() bool { return strings.Contains(out.String(), "localhost:"+itoa(port)) })
	time.Sleep(30 * time.Millisecond)
	if n := strings.Count(out.String(), "localhost:"+itoa(port)); n != 1 {
		t.Errorf("busy port logged %d times:\n%s", n, out.String())
	}
}

func TestRelayRefusesToRunTwice(t *testing.T) {
	b, _, _ := newTestBackend(t, newFake())
	release := holdRelayLock(t, b)
	defer release()
	if err := b.ServeForwards(context.Background()); !errors.Is(err, errRelayBusy) {
		t.Errorf("ServeForwards = %v, want errRelayBusy", err)
	}
}

func TestRelayDoesNotIdleOutWhileForwardsExistOrRecordsAreUnreadable(t *testing.T) {
	f := newFake()
	b, out := newRelayBackend(t, f, 2*time.Millisecond, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	createDev(t, b)
	port := freePort(t)
	if _, err := b.Forward(ctx, "dev", []vm.Forward{{Host: port, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.ServeForwards(ctx) }()
	waitFor(t, "the listener", func() bool { return listens(port) })
	// Records unreadable: the relay keeps what it serves and does not exit.
	if err := os.Chmod(b.paths.MachinesDir(), 0o000); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := os.Chmod(b.paths.MachinesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("relay exited (%v) while a forward existed:\n%s", err, out.String())
	default:
	}
	if !listens(port) {
		t.Error("the listener must survive an unreadable records directory")
	}
	// Forwards gone: now it exits on its own, and the lock is free again.
	if _, err := b.Unforward(ctx, "dev", []int{port}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ServeForwards = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not exit without forwards")
	}
	if alive, err := b.relayAlive(); err != nil || alive {
		t.Errorf("relay lock still held after exit: alive=%v err=%v", alive, err)
	}
}

// TestPipeHalfCloseAndTeardown: a client that half-closes still gets the
// whole answer, and a reset on one side ends both.
func TestPipeHalfCloseAndTeardown(t *testing.T) {
	f := newFake()
	b, _ := newRelayBackend(t, f, 5*time.Millisecond, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	createDev(t, b)
	// echo until EOF: the answer only comes after the client half-closed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				data, _ := io.ReadAll(conn)
				_, _ = conn.Write(bytes.ToUpper(data))
			}()
		}
	}()
	f.guestServers["192.168.64.2:80"] = ln.Addr().String()
	port := freePort(t)
	if _, err := b.Forward(ctx, "dev", []vm.Forward{{Host: port, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.ServeForwards(ctx) }()
	waitFor(t, "the listener", func() bool { return listens(port) })
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, "half"); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if got, err := io.ReadAll(conn); err != nil || string(got) != "HALF" {
		t.Errorf("after half-close: %q, %v", got, err)
	}
}

// TestRelayKeepsAFreshAddressAfterAClosedPort: a VM that runs but whose
// service is not up yet must not cost one `machine ls` per connection.
func TestRelayKeepsAFreshAddressAfterAClosedPort(t *testing.T) {
	f := newFake()
	b, _ := newRelayBackend(t, f, 5*time.Millisecond, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	createDev(t, b)
	port := freePort(t)
	if _, err := b.Forward(ctx, "dev", []vm.Forward{{Host: port, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = b.ServeForwards(ctx) }()
	waitFor(t, "the listener", func() bool { return listens(port) })
	// The probe above was the first connection: the relay resolved the
	// address for it and the guest refused port 80. Wait for that attempt
	// to finish (the relay closes the client), then count from zero.
	fake := f.runner
	if got, err := roundTrip(port, "x\n"); got != "" || (err != nil && !strings.Contains(err.Error(), "reset")) {
		t.Fatalf("closed guest port: got %q, %v", got, err)
	}
	waitFor(t, "the first resolution", func() bool { return strings.Contains(fake.Dump(), "machine ls") })
	fake.Reset()
	for i := 0; i < 5; i++ {
		if got, err := roundTrip(port, "x\n"); got != "" || (err != nil && !strings.Contains(err.Error(), "reset")) {
			t.Fatalf("closed guest port: got %q, %v", got, err)
		}
	}
	if n := strings.Count(fake.Dump(), "machine ls"); n != 0 {
		t.Errorf("a fresh address must be kept across refused connections, got %d machine ls:\n%s", n, fake.Dump())
	}
}
