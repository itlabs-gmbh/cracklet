// Package container is the backend that runs every microVM as its own
// Virtualization.framework VM through Apple's `container machine` CLI
// (github.com/apple/container). No nested virtualization, no shared runtime
// VM: the Mac reaches the guests directly on vmnet. See
// docs/design/container-backend.md.
package container

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/host"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
	"github.com/itlabs-gmbh/cracklet/internal/sshkey"
)

const (
	// cmdTimeout bounds every `container` call that should answer at once, so
	// a wedged apiserver cannot hang cracklet.
	cmdTimeout = 30 * time.Second
	// MinVersion is the first `container` release the backend was tested with
	// (machine subcommand, build context fix).
	MinVersion = "1.5.0"
)

// Dialer opens TCP connections; tests replace it.
type Dialer func(ctx context.Context, addr string) (backend.Conn, error)

// Backend implements backend.Backend on Apple's container CLI.
type Backend struct {
	r        runner.Runner
	paths    config.Paths
	out      io.Writer
	status   io.Writer
	lookPath host.LookPathFunc
	gather   func(ctx context.Context) host.Facts
	dial     Dialer
	exe      string // the cracklet binary, for the ssh ProxyCommand
	sshWait  time.Duration
	poll     time.Duration
}

// Option customises a Backend.
type Option func(*Backend)

// WithFacts overrides how host facts are gathered (used by tests).
func WithFacts(gather func(ctx context.Context) host.Facts) Option {
	return func(b *Backend) { b.gather = gather }
}

// WithLookPath overrides executable lookup (used by tests).
func WithLookPath(fn host.LookPathFunc) Option {
	return func(b *Backend) { b.lookPath = fn }
}

// WithDialer overrides how guest ports are reached (used by tests).
func WithDialer(d Dialer) Option {
	return func(b *Backend) { b.dial = d }
}

// WithExecutable overrides the path rendered into the ssh ProxyCommand.
func WithExecutable(path string) Option {
	return func(b *Backend) { b.exe = path }
}

// WithSSHWait overrides how long a booting machine may take to accept ssh.
func WithSSHWait(wait, poll time.Duration) Option {
	return func(b *Backend) { b.sshWait, b.poll = wait, poll }
}

// New wires a Backend. out receives command output, status progress notes.
func New(r runner.Runner, paths config.Paths, out, status io.Writer, opts ...Option) *Backend {
	b := &Backend{r: r, paths: paths, out: out, status: status, lookPath: exec.LookPath,
		dial: tcpDial, exe: "cracklet", sshWait: 60 * time.Second, poll: 100 * time.Millisecond}
	if exe, err := os.Executable(); err == nil {
		b.exe = exe
	}
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
func (b *Backend) Name() string { return backend.NameContainer }

// Capabilities implements backend.Backend: every boot is cold (0.5 s), the
// root disk is a sparse 512 GiB volume, forwards and the guest firewall come
// in later phases, and there is no shared runtime VM to resize.
func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{}
}

func (b *Backend) printf(format string, args ...any) { fmt.Fprintf(b.out, format, args...) }

func (b *Backend) statusf(format string, args ...any) { fmt.Fprintf(b.status, format, args...) }

// run executes a `container` command that must answer promptly and returns
// its stdout.
func (b *Backend) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	out, err := b.r.Output(ctx, "container", args...)
	if err != nil {
		return nil, fmt.Errorf("container %s: %w", args[0], err)
	}
	return out, nil
}

// --- host and runtime state ------------------------------------------------

// checkHost adds the backend's own requirements to the shared host checks:
// macOS 26 (the only release `container` supports) and the CLI itself. The
// shared nested-virtualization checks (M3, macOS 15) do not apply here.
func checkHost(f host.Facts) []host.Problem {
	if f.OS != "darwin" || f.Arch != "arm64" {
		return []host.Problem{{Code: "platform", Fatal: true,
			Message: fmt.Sprintf("cracklet needs macOS on Apple Silicon, found %s/%s", f.OS, f.Arch)}}
	}
	var problems []host.Problem
	if major, err := host.MajorVersion(f.MacOSVersion); err != nil || major < 26 {
		problems = append(problems, host.Problem{Code: "macos-version", Fatal: true,
			Message: fmt.Sprintf("macOS 26 or newer is required by Apple's container, found %q", f.MacOSVersion)})
	}
	if !f.HVSupport {
		problems = append(problems, host.Problem{Code: "hv-support", Fatal: true,
			Message: "the Hypervisor framework is not available (kern.hv_support != 1)"})
	}
	if !f.HasContainer {
		p := host.Problem{Code: "container-missing", Message: "Apple's container CLI is not installed", Fatal: !f.HasBrew}
		if f.HasBrew {
			p.Message += "; it will be installed with 'brew install container'"
		} else {
			p.Message += " and Homebrew is unavailable to install it (https://github.com/apple/container/releases)"
		}
		problems = append(problems, p)
	}
	return problems
}

// systemStatus parses `container system status` (FIELD VALUE lines).
func (b *Backend) systemStatus(ctx context.Context) (map[string]string, error) {
	out, err := b.run(ctx, "system", "status")
	if err != nil {
		return nil, err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			fields[k] = strings.TrimSpace(v)
		}
	}
	return fields, nil
}

// Alive implements backend.Backend.
func (b *Backend) Alive(ctx context.Context) error {
	st, err := b.systemStatus(ctx)
	if err != nil {
		return fmt.Errorf("the container system service is not running, run 'cracklet prepare' or 'container system start' (%w)", err)
	}
	if st["status"] != "running" {
		return fmt.Errorf("the container system service is %q, run 'container system start'", st["status"])
	}
	return nil
}

// Ready implements backend.Backend: it starts the system service when it is
// down (as after a reboot of the Mac) and brings back the machines that ran
// before, once, however many commands run in parallel.
func (b *Backend) Ready(ctx context.Context) error {
	if err := b.Alive(ctx); err == nil {
		return nil
	}
	release, err := b.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := b.Alive(ctx); err == nil {
		return nil // another command started it while this one waited
	}
	b.statusf("==> Starting the container system service\n")
	if _, err := b.run(ctx, "system", "start", "--enable-kernel-install"); err != nil {
		return err
	}
	if err := b.restoreMachines(ctx); err != nil {
		b.statusf("  [WARN] the service is running again, but restarting its microVMs failed: %v\n"+
			"         check 'cracklet ls' and start them with 'cracklet start NAME'\n", err)
	}
	return nil
}

// ReadyToBoot implements backend.Backend. There is no shared runtime VM that a
// resize could restart underneath a boot, so there is nothing to hold.
func (b *Backend) ReadyToBoot(ctx context.Context) (func(), error) {
	if err := b.Ready(ctx); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Doctor implements backend.Backend.
func (b *Backend) Doctor(ctx context.Context) error {
	facts := b.gather(ctx)
	b.printf("macOS %s on %s (%s/%s), hypervisor=%v, brew=%v, container=%v\n",
		facts.MacOSVersion, facts.Chip, facts.OS, facts.Arch, facts.HVSupport, facts.HasBrew, facts.HasContainer)
	problems := checkHost(facts)
	for _, p := range problems {
		b.printf("  [%s] %s\n", severity(p), p.Message)
	}
	if host.HasFatal(problems) {
		return fmt.Errorf("host is not ready for cracklet")
	}
	if !facts.HasContainer {
		return nil
	}
	version, err := b.version(ctx)
	if err != nil {
		return err
	}
	b.printf("container %s (needs %s or newer)\n", version, MinVersion)
	st, err := b.systemStatus(ctx)
	if err != nil {
		b.printf("container system service: not running (run 'cracklet prepare')\n")
		return nil
	}
	b.printf("container system service: %s\n", st["status"])
	return nil
}

// Prepare implements backend.Backend: host check, CLI, system service, guest
// image. Idempotent; the image is rebuilt only when its labels do not match.
func (b *Backend) Prepare(ctx context.Context, o backend.PrepareOptions) error {
	if o.Explicit != (backend.SizeFlags{}) {
		return fmt.Errorf("--cpus, --memory and --disk size the shared Lima VM; the container backend has none (size VMs with 'cracklet new --vcpus/--mem')")
	}
	if len(o.Profiles) > 0 {
		b.printf("  note: profiles size the root disk of Firecracker images; container machines get a sparse disk and ignore --profile\n")
	}
	if err := b.preflight(ctx); err != nil {
		return err
	}
	if err := sshkey.Ensure(ctx, b.r, b.paths, b.out); err != nil {
		return err
	}
	if err := b.Alive(ctx); err != nil {
		b.printf("==> Starting the container system service\n")
		if _, err := b.run(ctx, "system", "start", "--enable-kernel-install"); err != nil {
			return err
		}
	}
	return b.ensureImage(ctx)
}

func (b *Backend) preflight(ctx context.Context) error {
	b.printf("==> Checking host\n")
	facts := b.gather(ctx)
	problems := checkHost(facts)
	for _, p := range problems {
		b.printf("  [%s] %s\n", severity(p), p.Message)
	}
	if host.HasFatal(problems) {
		return fmt.Errorf("host is not ready for cracklet")
	}
	if !facts.HasContainer {
		b.printf("==> Installing Apple's container CLI\n")
		if err := b.r.Run(ctx, "brew", "install", "container"); err != nil {
			return fmt.Errorf("install container: %w", err)
		}
	}
	version, err := b.version(ctx)
	if err != nil {
		return err
	}
	if olderThan(version, MinVersion) {
		return fmt.Errorf("container %s is too old, cracklet needs %s or newer (brew upgrade container)", version, MinVersion)
	}
	return nil
}

// version parses "container CLI version 1.5.0 (build: release, commit: ...)".
func (b *Backend) version(ctx context.Context) (string, error) {
	out, err := b.run(ctx, "--version")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "version" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("cannot parse container version from %q", strings.TrimSpace(string(out)))
}

// olderThan compares dotted versions numerically, field by field.
func olderThan(have, want string) bool {
	h, w := strings.Split(have, "."), strings.Split(want, ".")
	for i := 0; i < len(h) || i < len(w); i++ {
		var hv, wv int
		if i < len(h) {
			fmt.Sscanf(h[i], "%d", &hv)
		}
		if i < len(w) {
			fmt.Sscanf(w[i], "%d", &wv)
		}
		if hv != wv {
			return hv < wv
		}
	}
	return false
}

func severity(p host.Problem) string {
	if p.Fatal {
		return "ERROR"
	}
	return "WARN"
}
