package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/vm"
)

const (
	// defaultRelayPoll is how often the relay re-reads the records.
	defaultRelayPoll = time.Second
	// defaultRelayIdle is how long the relay waits without any forward
	// before it exits; the next forward starts it again.
	defaultRelayIdle = 2 * time.Minute
	// relayAddrTTL is how long a resolved guest address is reused before
	// `machine ls` is asked again, so a burst of connections (a browser)
	// does not spawn one CLI call each; relayNegTTL is the same for a VM
	// that was not running, short so a `start` is noticed at once.
	relayAddrTTL = 5 * time.Second
	relayNegTTL  = time.Second
	// relayDialTimeout bounds the connection to the guest port.
	relayDialTimeout = 5 * time.Second
	// acceptBackoff is the pause after an Accept error that is not a close
	// (out of file descriptors), as net/http does.
	acceptBackoff = 100 * time.Millisecond
)

// errRelayBusy means another relay serves this state directory.
var errRelayBusy = errors.New("another relay is already running for this SKIFF_HOME")

// WithRelayTiming overrides how often the relay looks at the records and how
// long it idles before exiting (used by tests).
func WithRelayTiming(poll, idle time.Duration) Option {
	return func(b *Backend) { b.relayPoll, b.relayIdle = poll, idle }
}

// relayListener is one served host port.
type relayListener struct {
	ln     net.Listener
	target forwardTarget
}

// ServeForwards implements backend.ForwardRelay: it is the body of
// `skiff _relay`. Every recorded forward gets a listener on
// 127.0.0.1:HOST; each accepted connection is joined to the VM's guest port
// through the backend's dialer, so the relay follows a VM to whatever
// address it has after a restart and simply closes connections while it is
// stopped. The records are the only input: forward, unforward and rm change
// them, and the relay follows within one poll. It holds relay.lock while it
// runs (ensureRelay checks it) and exits after relayIdle without forwards.
func (b *Backend) ServeForwards(ctx context.Context) error {
	release, err := b.holdRelayLock()
	if err != nil {
		return err
	}
	defer release()
	raiseFileLimit()
	listeners := map[int]*relayListener{}
	failed := map[int]bool{}
	defer func() {
		for _, l := range listeners {
			_ = l.ln.Close()
		}
	}()
	var idleSince time.Time
	for {
		targets, err := b.forwardTargets()
		switch {
		case err != nil:
			b.relayLog("cannot read forwards: %v", err)
		case len(targets) > 0:
			idleSince = time.Time{}
			b.reconcile(ctx, listeners, failed, targets)
		case idleSince.IsZero():
			idleSince = time.Now()
			b.reconcile(ctx, listeners, failed, targets)
		case time.Since(idleSince) >= b.relayIdle:
			if exit, err := b.idleExit(ctx, release); err != nil {
				b.relayLog("%v", err)
			} else if exit {
				return nil
			}
			idleSince = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(b.relayPoll):
		}
	}
}

// idleExit gives the relay lock up and reports true if there is still nothing
// to serve. It decides under the backend lock, which Forward holds while it
// records a forward and checks for a running relay, so a forward can never
// land between the last look at the records and the exit.
func (b *Backend) idleExit(ctx context.Context, release func()) (bool, error) {
	unlock, err := b.lock(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	targets, err := b.forwardTargets()
	if err != nil || len(targets) > 0 {
		return false, err
	}
	b.relayLog("no forwards for %s, exiting", b.relayIdle)
	release()
	return true, nil
}

// holdRelayLock takes relay.lock for the lifetime of the relay.
func (b *Backend) holdRelayLock() (func(), error) {
	if err := os.MkdirAll(b.paths.Home, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", b.paths.Home, err)
	}
	f, err := os.OpenFile(b.paths.RelayLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", b.paths.RelayLockPath(), err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errRelayBusy
		}
		return nil, fmt.Errorf("lock %s: %w", b.paths.RelayLockPath(), err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		})
	}, nil
}

// raiseFileLimit lifts the soft descriptor limit to the hard one: every
// relayed connection costs two, and a shell's default of 256 is a browser
// tab away.
func raiseFileLimit() {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil || lim.Cur >= lim.Max {
		return
	}
	lim.Cur = lim.Max
	_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &lim)
}

// reconcile closes listeners whose forward is gone or retargeted and opens
// the missing ones. A host port that cannot be bound is reported once and
// retried on every poll, so a port freed later starts working on its own.
func (b *Backend) reconcile(ctx context.Context, listeners map[int]*relayListener, failed map[int]bool, targets map[int]forwardTarget) {
	for port, l := range listeners {
		if t, ok := targets[port]; !ok || t != l.target {
			_ = l.ln.Close()
			delete(listeners, port)
			b.relayLog("localhost:%d -> %s:%d closed", port, l.target.VM, l.target.Guest)
		}
	}
	for port := range failed {
		if _, ok := targets[port]; !ok {
			delete(failed, port)
		}
	}
	for port, t := range targets {
		if _, ok := listeners[port]; ok {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			if !failed[port] {
				b.relayLog("localhost:%d -> %s:%d: %v (retrying)", port, t.VM, t.Guest, err)
				failed[port] = true
			}
			continue
		}
		delete(failed, port)
		listeners[port] = &relayListener{ln: ln, target: t}
		b.relayLog("localhost:%d -> %s:%d listening", port, t.VM, t.Guest)
		go b.accept(ctx, ln, t)
	}
}

// accept serves a listener until it is closed. Any other Accept error (out
// of descriptors) is logged and retried after a pause: the port stays
// bound and must not go silently dead.
func (b *Backend) accept(ctx context.Context, ln net.Listener, t forwardTarget) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			b.relayLog("localhost:%d: accept: %v", t.hostPort(ln), err)
			if !sleepCtx(ctx, acceptBackoff) {
				return
			}
			continue
		}
		go b.serveConn(ctx, conn, t)
	}
}

func (forwardTarget) hostPort(ln net.Listener) int {
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// serveConn joins one accepted connection to the guest port, or closes it
// when the guest cannot be reached (stopped VM, port not listening).
func (b *Backend) serveConn(ctx context.Context, client net.Conn, t forwardTarget) {
	tcp, ok := client.(backend.Conn)
	if !ok {
		_ = client.Close()
		return
	}
	guest, err := b.dialGuest(ctx, t)
	if err != nil {
		_ = client.Close()
		return
	}
	pipe(tcp, guest)
}

// dialGuest connects to the guest port. The VM's address is resolved through
// `machine ls` at most once per relayAddrTTL, one resolution at a time, and
// a VM that is not running is remembered for relayNegTTL, so a burst of
// connections costs one CLI call, not one each. A failed connection to an
// address resolved more than relayNegTTL ago forgets it, so a VM restarted
// under a new one is found again within a second, while a service that is
// not up yet on a fresh address does not cost a CLI call per attempt.
// Failures are logged when an address is resolved or dropped, not per
// connection.
func (b *Backend) dialGuest(ctx context.Context, t forwardTarget) (backend.Conn, error) {
	ip, err := b.resolve(ctx, t.VM)
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, relayDialTimeout)
	defer cancel()
	conn, err := b.dial(dctx, net.JoinHostPort(ip, strconv.Itoa(t.Guest)))
	if err != nil {
		if b.addrs.forgetStale(t.VM, relayNegTTL) {
			b.relayLog("%s:%d: %v", t.VM, t.Guest, err)
		}
		return nil, err
	}
	return conn, nil
}

func (b *Backend) resolve(ctx context.Context, name string) (string, error) {
	b.addrs.resolving.Lock()
	defer b.addrs.resolving.Unlock()
	if c, ok := b.addrs.get(name); ok {
		return c.ip, c.err
	}
	m, err := b.machine(ctx, name)
	if err == nil && (m.state() != vm.StateRunning || m.addr() == "") {
		err = fmt.Errorf("VM '%s' is %s", name, m.state())
	}
	if err != nil {
		b.relayLog("%s: %v", name, err)
		b.addrs.put(name, cachedAddr{err: err})
		return "", err
	}
	b.addrs.put(name, cachedAddr{ip: m.addr()})
	return m.addr(), nil
}

// addrCache remembers how a VM was last resolved for the relay.
type addrCache struct {
	mu        sync.Mutex
	resolving sync.Mutex // one `machine ls` at a time
	addrs     map[string]cachedAddr
}

type cachedAddr struct {
	ip  string
	err error
	at  time.Time
}

func (c *addrCache) get(name string) (cachedAddr, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.addrs[name]
	ttl := relayAddrTTL
	if a.err != nil {
		ttl = relayNegTTL
	}
	if !ok || time.Since(a.at) > ttl {
		return cachedAddr{}, false
	}
	return a, true
}

func (c *addrCache) put(name string, a cachedAddr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.addrs == nil {
		c.addrs = map[string]cachedAddr{}
	}
	a.at = time.Now()
	c.addrs[name] = a
}

// forgetStale drops the entry for name if it is older than age and reports
// whether it did.
func (c *addrCache) forgetStale(name string, age time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.addrs[name]
	if !ok || time.Since(a.at) <= age {
		return false
	}
	delete(c.addrs, name)
	return true
}

// pipe copies both ways until both sides are done. A direction that ends
// cleanly (EOF) is half-closed, so a guest that waits for EOF (an HTTP
// server reading a request body) sees it while its answer is still relayed;
// one that ends in an error (a reset) tears the whole connection down, or
// the other side would sit in Read until its peer gives up.
func pipe(client, guest backend.Conn) {
	done := make(chan struct{}, 2)
	copyDir := func(dst, src backend.Conn) {
		if _, err := io.Copy(dst, src); err != nil {
			_ = client.Close()
			_ = guest.Close()
		} else {
			_ = dst.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyDir(guest, client)
	go copyDir(client, guest)
	<-done
	<-done
	_ = client.Close()
	_ = guest.Close()
}

// sleepCtx waits d and reports false if ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// relayLog writes a timestamped line to the relay's log (status, which the
// spawner sends to relay.log).
func (b *Backend) relayLog(format string, args ...any) {
	b.statusf("%s relay: %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
