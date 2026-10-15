// Package lima is the backend that runs Firecracker microVMs inside a Lima VM
// with nested virtualization, driven through limactl and the guest agent
// (internal/agent). It is scheduled for removal once the apple/container
// backend covers its features; see docs/design/container-backend.md.
package lima

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/itlabs-gmbh/skiff/internal/agent"
	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/envdbin"
	"github.com/itlabs-gmbh/skiff/internal/host"
	limactl "github.com/itlabs-gmbh/skiff/internal/lima"
	"github.com/itlabs-gmbh/skiff/internal/runner"
)

// EnvdSource yields the linux/arm64 skiff-envd binary that Prepare ships
// into the Lima VM.
type EnvdSource func(ctx context.Context) ([]byte, error)

// Backend implements backend.Backend on a single Lima instance.
type Backend struct {
	r        runner.Runner
	lima     *limactl.Client
	paths    config.Paths
	out      io.Writer
	status   io.Writer // progress that must not mix into a command's output
	lookPath host.LookPathFunc
	gather   func(ctx context.Context) host.Facts
	envd     EnvdSource

	agentMu sync.Mutex
	agentOK bool // the guest agent was verified current in this process
}

// Option customises a Backend.
type Option func(*Backend)

// WithEnvd overrides where the guest daemon binary comes from (used by tests).
func WithEnvd(src EnvdSource) Option {
	return func(b *Backend) { b.envd = src }
}

// WithLookPath overrides executable lookup for the preflight check and the
// envd cross-compile (used by tests).
func WithLookPath(fn host.LookPathFunc) Option {
	return func(b *Backend) { b.lookPath = fn }
}

// WithFacts overrides how host facts are gathered for the preflight check
// (used by tests so they do not depend on the CI runner).
func WithFacts(gather func(ctx context.Context) host.Facts) Option {
	return func(b *Backend) { b.gather = gather }
}

// New wires a Backend. out receives command output, status progress notes.
func New(r runner.Runner, paths config.Paths, out, status io.Writer, opts ...Option) *Backend {
	b := &Backend{r: r, lima: limactl.NewClient(r, config.Instance), paths: paths, out: out, status: status, lookPath: exec.LookPath}
	b.envd = b.loadEnvd
	for _, opt := range opts {
		opt(b)
	}
	if b.gather == nil {
		b.gather = func(ctx context.Context) host.Facts {
			return host.Gatherer{Runner: r, LookPath: b.lookPath}.Gather(ctx)
		}
	}
	return b
}

var _ backend.Backend = (*Backend)(nil)

// Name implements backend.Backend.
func (b *Backend) Name() string { return "lima" }

// Capabilities implements backend.Backend.
func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{Snapshots: true, Profiles: true, Forwards: true, GuestFirewall: true, Resize: true}
}

func (b *Backend) printf(format string, args ...any) {
	fmt.Fprintf(b.out, format, args...)
}

func (b *Backend) statusf(format string, args ...any) {
	fmt.Fprintf(b.status, format, args...)
}

// loadEnvd prefers the daemon embedded by `make envd`; a skiff installed via
// `go install ...@latest` has none and cross-compiles it from the module cache.
func (b *Backend) loadEnvd(ctx context.Context) ([]byte, error) {
	if data, ok := envdbin.Embedded(); ok {
		return data, nil
	}
	b.printf("==> skiff-envd is not embedded in this build, cross-compiling it with the local Go toolchain\n")
	return envdbin.Builder{Runner: b.r, LookPath: b.lookPath, Version: envdbin.RunningVersion()}.Build(ctx)
}

// LoadEnvd exposes the default envd source (used by tests).
func (b *Backend) LoadEnvd(ctx context.Context) ([]byte, error) { return b.loadEnvd(ctx) }

// --- guest agent -------------------------------------------------------------

// ensureAgent installs or refreshes the guest agent when its checksum differs.
// It probes once per process: the agent is a file on the Lima disk that only
// skiff itself replaces.
func (b *Backend) ensureAgent(ctx context.Context) error {
	b.agentMu.Lock()
	defer b.agentMu.Unlock()
	if b.agentOK {
		return nil
	}
	// The agent lives in a root-owned directory, so the probe needs sudo too.
	probe := "sha256sum " + config.AgentPath + " 2>/dev/null | cut -d' ' -f1"
	out, err := b.lima.ShellOutput(ctx, "sudo", "sh", "-c", probe)
	if err != nil {
		return fmt.Errorf("probe guest agent: %w", err)
	}
	if strings.TrimSpace(string(out)) != agent.Checksum() {
		if err := b.lima.WriteFile(ctx, strings.NewReader(agent.Script), config.AgentPath, "0755"); err != nil {
			return fmt.Errorf("install guest agent: %w", err)
		}
	}
	b.agentOK = true
	return nil
}

// agentOutput runs an agent subcommand and returns its stdout (JSON).
func (b *Backend) agentOutput(ctx context.Context, args ...string) ([]byte, error) {
	if err := b.ensureAgent(ctx); err != nil {
		return nil, err
	}
	full := append([]string{"sudo", config.AgentPath}, args...)
	out, err := b.lima.ShellOutput(ctx, full...)
	if err != nil {
		return nil, fmt.Errorf("agent %s: %w", args[0], err)
	}
	return out, nil
}

// agentRun runs a long-lived agent subcommand with the terminal attached.
func (b *Backend) agentRun(ctx context.Context, args ...string) error {
	if err := b.ensureAgent(ctx); err != nil {
		return err
	}
	full := append([]string{"sudo", config.AgentPath}, args...)
	if err := b.lima.Shell(ctx, full...); err != nil {
		return fmt.Errorf("agent %s: %w", args[0], err)
	}
	return nil
}

// --- Ready / Alive -----------------------------------------------------------

// errLimaStopped marks a Lima instance that Ready may start.
var errLimaStopped = errors.New("stopped")

// Ready implements backend.Backend. A stopped instance, as after a reboot of
// the Mac, is started and the microVMs that ran before it stopped come back;
// anything else needs 'skiff prepare'.
func (b *Backend) Ready(ctx context.Context) error {
	err := b.Alive(ctx)
	switch {
	case errors.Is(err, errLimaStopped):
		return b.bootLima(ctx)
	case err != nil:
		return err
	}
	// Another command may have started it and still restore its microVMs;
	// the boot lock is only held for that, so this rarely waits.
	release, err := b.WaitForBoot(ctx)
	if err != nil {
		return err
	}
	release()
	return nil
}

// ReadyToBoot implements backend.Backend: the shared Lima lock is taken
// before the first limactl call, so a start during a resize touches nothing.
// The agent is verified here rather than lazily in Create, so that an
// interrupted probe fails before the caller counts the creation as started
// (and cleans up a VM of that name that already existed).
func (b *Backend) ReadyToBoot(ctx context.Context) (func(), error) {
	release, err := b.HoldForMicroVM()
	if err != nil {
		return nil, err
	}
	if err := b.Ready(ctx); err != nil {
		release()
		return nil, err
	}
	if err := b.ensureAgent(ctx); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// Alive implements backend.Backend.
func (b *Backend) Alive(ctx context.Context) error {
	inst, ok, err := b.lima.Get(ctx)
	switch {
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("Lima instance %q does not exist yet, run 'skiff prepare' first", config.Instance)
	case inst.Status == limactl.StatusRunning:
		return nil
	case inst.Status == limactl.StatusStopped:
		return fmt.Errorf("Lima instance %q is %w, run 'skiff prepare' to start it", config.Instance, errLimaStopped)
	}
	return fmt.Errorf("Lima instance %q is %s, run 'skiff prepare' to start it", config.Instance, inst.Status)
}

// bootLima starts the stopped Lima VM and waits until its microVMs are back.
func (b *Backend) bootLima(ctx context.Context) error {
	// A resize stops the Lima VM on purpose; it must not be started under it.
	hold, err := b.HoldForMicroVM()
	if err != nil {
		return err
	}
	defer hold()
	release, err := b.WaitForBoot(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Another skiff command may have started it while this one waited.
	inst, ok, err := b.lima.Get(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Lima instance %q was removed, run 'skiff prepare' to create it", config.Instance)
	}
	if inst.Status != limactl.StatusRunning {
		b.statusf("==> Starting Lima VM %q\n", config.Instance)
		if err := b.lima.Start(ctx); err != nil {
			return err
		}
	}
	// The Lima VM is up, so the command itself can go on; a VM that is not
	// back yet is reported and can be started by hand.
	if err := b.restoreMicroVMs(ctx); err != nil {
		b.statusf("  [WARN] the Lima VM is running again, but restarting its microVMs failed: %v\n"+
			"         check 'skiff ls' and start them with 'skiff start NAME'\n", err)
	}
	return nil
}
