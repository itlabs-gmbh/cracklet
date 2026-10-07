// Package app implements the cracklet workflows on top of Lima and the guest agent.
package app

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/envdbin"
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
	platform hostPlatform
	probe    PortProbe
	portBusy PortCheck
	envd     EnvdSource
	tunnel   Tunnel
	// tunnelRetry is the pause before `cracklet tunnel` reconnects.
	tunnelRetry time.Duration
	// brokerWatch is how often a session sharing another session's broker
	// checks whether it is still there.
	brokerWatch time.Duration
}

// EnvdSource yields the linux/arm64 cracklet-envd binary that Prepare ships
// into the Lima VM.
type EnvdSource func(ctx context.Context) ([]byte, error)

// PortProbe reports whether 127.0.0.1:port on the Mac accepts connections,
// waiting a little for Lima to expose a freshly forwarded port.
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

// WithPortProbe overrides the host port readiness check (used by tests).
func WithPortProbe(p PortProbe) Option {
	return func(a *App) { a.probe = p }
}

// WithPortCheck overrides the "port already in use" check (used by tests).
func WithPortCheck(c PortCheck) Option {
	return func(a *App) { a.portBusy = c }
}

// WithEnvd overrides where the guest daemon binary comes from (used by tests).
func WithEnvd(src EnvdSource) Option {
	return func(a *App) { a.envd = src }
}

// New wires an App.
func New(r runner.Runner, paths config.Paths, out io.Writer, opts ...Option) *App {
	a := &App{r: r, lima: lima.NewClient(r, config.Instance), paths: paths, out: out,
		lookPath: exec.LookPath, probe: waitForHostPort, portBusy: hostPortBusy, tunnelRetry: defaultTunnelRetry,
		brokerWatch: defaultBrokerWatch}
	a.envd = a.loadEnvd
	a.tunnel = a.startBroker
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *App) gatherFacts(ctx context.Context) host.Facts {
	return host.Gatherer{OS: a.platform.os, Arch: a.platform.arch, Runner: a.r, LookPath: a.lookPath}.Gather(ctx)
}

// loadEnvd prefers the daemon embedded by `make envd`; a cracklet installed via
// `go install ...@latest` has none and cross-compiles it from the module cache.
func (a *App) loadEnvd(ctx context.Context) ([]byte, error) {
	if data, ok := envdbin.Embedded(); ok {
		return data, nil
	}
	a.printf("==> cracklet-envd is not embedded in this build, cross-compiling it with the local Go toolchain\n")
	return envdbin.Builder{Runner: a.r, LookPath: a.lookPath, Version: envdbin.RunningVersion()}.Build(ctx)
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
