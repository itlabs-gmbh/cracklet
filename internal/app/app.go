// Package app implements the cracklet workflows on top of Lima and the guest agent.
package app

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/host"
	"github.com/itlabs-gmbh/cracklet/internal/lima"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// App bundles the collaborators every command needs.
type App struct {
	r        runner.Runner
	lima     *lima.Client
	paths    config.Paths
	out      io.Writer
	lookPath host.LookPathFunc
	probe    PortProbe
	portBusy PortCheck
}

// PortProbe reports whether 127.0.0.1:port on the Mac accepts connections,
// waiting a little for Lima to expose a freshly forwarded port.
type PortProbe func(ctx context.Context, port int) bool

// PortCheck reports immediately whether something already listens on
// 127.0.0.1:port on the Mac.
type PortCheck func(port int) bool

// Option customises an App.
type Option func(*App)

// WithLookPath overrides executable lookup (used by tests).
func WithLookPath(fn host.LookPathFunc) Option {
	return func(a *App) { a.lookPath = fn }
}

// WithPortProbe overrides the host port readiness check (used by tests).
func WithPortProbe(p PortProbe) Option {
	return func(a *App) { a.probe = p }
}

// WithPortCheck overrides the "port already in use" check (used by tests).
func WithPortCheck(c PortCheck) Option {
	return func(a *App) { a.portBusy = c }
}

// New wires an App.
func New(r runner.Runner, paths config.Paths, out io.Writer, opts ...Option) *App {
	a := &App{r: r, lima: lima.NewClient(r, config.Instance), paths: paths, out: out,
		lookPath: exec.LookPath, probe: waitForHostPort, portBusy: hostPortBusy}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *App) gatherFacts(ctx context.Context) host.Facts {
	return host.Gatherer{Runner: a.r, LookPath: a.lookPath}.Gather(ctx)
}

func (a *App) printf(format string, args ...any) {
	fmt.Fprintf(a.out, format, args...)
}

// requireRunning fails with a helpful hint unless the Lima instance is up.
func (a *App) requireRunning(ctx context.Context) error {
	inst, ok, err := a.lima.Get(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Lima instance %q does not exist yet, run 'cracklet prepare' first", config.Instance)
	}
	if inst.Status != lima.StatusRunning {
		return fmt.Errorf("Lima instance %q is %s, run 'cracklet prepare' to start it", config.Instance, inst.Status)
	}
	return nil
}
