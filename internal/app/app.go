// Package app implements the skiff workflows on top of a backend that
// creates the VMs and the guest they expose over ssh.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/backend/container"
	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/host"
	"github.com/itlabs-gmbh/skiff/internal/runner"
)

// App bundles the collaborators every command needs.
type App struct {
	r       runner.Runner
	backend backend.Backend
	// newBackend builds the backend from an App's runner, writers and
	// options; standby calls it on a silenced copy of the App.
	newBackend func(*App) backend.Backend
	paths      config.Paths
	out        io.Writer
	status     io.Writer // progress that must not mix into a command's output
	lookPath   host.LookPathFunc
	platform   hostPlatform
	probe      PortProbe
	portBusy   PortCheck
	tunnel     Tunnel
	// tunnelRetry is the pause before `skiff tunnel` reconnects.
	tunnelRetry time.Duration
	// brokerWatch is how often a session sharing another session's broker
	// checks whether it is still there.
	brokerWatch time.Duration
	// now is the clock gc measures VM ages against.
	now func() time.Time
}

// PortProbe reports whether 127.0.0.1:port on the Mac accepts connections,
// waiting a little for the relay to serve a freshly forwarded port.
type PortProbe func(ctx context.Context, port int) bool

// PortCheck reports immediately whether something already listens on
// 127.0.0.1:port on the Mac.
type PortCheck func(port int) bool

// hostPlatform overrides the GOOS/GOARCH the preflight sees; empty means
// "whatever this binary runs on".
type hostPlatform struct {
	os   string
	arch string
}

// Option customises an App.
type Option func(*App)

// WithPlatform overrides the host OS and architecture reported to the
// preflight check (used by tests so they do not depend on the CI runner).
func WithPlatform(os, arch string) Option {
	return func(a *App) { a.platform = hostPlatform{os: os, arch: arch} }
}

// WithLookPath overrides executable lookup (used by tests).
func WithLookPath(fn host.LookPathFunc) Option {
	return func(a *App) { a.lookPath = fn }
}

// WithStatus overrides where progress notes such as starting the runtime go
// (stderr by default, so piped output of ssh and exec stays clean).
func WithStatus(w io.Writer) Option {
	return func(a *App) { a.status = w }
}

// WithPortProbe overrides the host port readiness check (used by tests).
func WithPortProbe(p PortProbe) Option {
	return func(a *App) { a.probe = p }
}

// WithPortCheck overrides the "port already in use" check (used by tests).
func WithPortCheck(c PortCheck) Option {
	return func(a *App) { a.portBusy = c }
}

// WithBackend replaces the container backend New would otherwise wire (used
// by tests). The same instance also serves the silenced standby copy of the
// App, and WithLookPath no longer reaches a backend.
func WithBackend(b backend.Backend) Option {
	return func(a *App) { a.newBackend = func(*App) backend.Backend { return b } }
}

// New wires an App on the container backend unless WithBackend replaces it.
func New(r runner.Runner, paths config.Paths, out io.Writer, opts ...Option) *App {
	a := &App{r: r, paths: paths, out: out, status: os.Stderr,
		lookPath: exec.LookPath, probe: waitForHostPort, portBusy: hostPortBusy, tunnelRetry: defaultTunnelRetry,
		brokerWatch: defaultBrokerWatch, now: time.Now}
	for _, opt := range opts {
		opt(a)
	}
	if a.newBackend == nil {
		a.newBackend = containerBackend
	}
	a.backend = a.newBackend(a)
	return a
}

// containerBackend builds the apple/container backend.
func containerBackend(a *App) backend.Backend {
	return container.New(a.r, a.paths, a.out, a.status,
		container.WithFacts(a.gatherFacts), container.WithLookPath(a.lookPath))
}

func (a *App) gatherFacts(ctx context.Context) host.Facts {
	return host.Gatherer{OS: a.platform.os, Arch: a.platform.arch, Runner: a.r, LookPath: a.lookPath}.Gather(ctx)
}

func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}
