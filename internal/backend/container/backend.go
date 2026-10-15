// Package container is the backend that runs every microVM as its own
// Virtualization.framework VM through Apple's `container machine` CLI
// (github.com/apple/container). No nested virtualization, no shared runtime
// VM: the Mac reaches the guests directly on vmnet. See
// docs/design/container-backend.md.
package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
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
	exe      string // the cracklet binary, for the ssh ProxyCommand and the relay
	sshWait  time.Duration
	poll     time.Duration
	// relayPoll and relayIdle pace ServeForwards; addrs is its address
	// cache; spawn starts it as a detached process.
	relayPoll time.Duration
	relayIdle time.Duration
	addrs     addrCache
	spawn     Spawner
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
		dial: tcpDial, exe: "cracklet", sshWait: 60 * time.Second, poll: 100 * time.Millisecond,
		relayPoll: defaultRelayPoll, relayIdle: defaultRelayIdle, spawn: spawnRelay}
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

// Capabilities implements backend.Backend: no snapshots (every boot is cold),
// no profiles (the root disk is a sparse 512 GiB volume), no shared runtime
// VM to resize. Forwards are served by the relay; the guest firewall is the
// nftables guard rail baked into the image (firewall.go), which root inside
// the guest can lift, unlike Lima's host-side rules.
func (b *Backend) Capabilities() backend.Capabilities {
	return backend.Capabilities{Forwards: true, GuestFirewall: true}
}

func (b *Backend) printf(format string, args ...any) { fmt.Fprintf(b.out, format, args...) }

func (b *Backend) statusf(format string, args ...any) { fmt.Fprintf(b.status, format, args...) }

// run executes a `container` query that must answer promptly (ls, status,
// inspect, --version) and returns its stdout.
func (b *Backend) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	return b.runLong(ctx, args...)
}

// runLong executes a `container` command that may take as long as it needs:
// a first `system start` downloads the kernel, `machine create` unpacks the
// image, `machine stop` waits for a graceful shutdown. Only ctx ends them.
func (b *Backend) runLong(ctx context.Context, args ...string) ([]byte, error) {
	out, err := b.r.Output(ctx, "container", args...)
	if err != nil {
		return nil, fmt.Errorf("container %s: %w", strings.Join(args[:min(len(args), 2)], " "), err)
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

// --enable-kernel-install lets `container system start` fetch Apple's default
// guest kernel. cracklet does not pin a digest for it, unlike the Firecracker
// artifacts of the Lima backend: the kernel comes from the same signed Apple
// release as the CLI that cracklet already trusts to run the VMs, and the CLI
// verifies the download itself. Pinning our own kernel is a Phase 4 option.
var systemStartArgs = []string{"system", "start", "--enable-kernel-install"}

// Ready implements backend.Backend: it starts the system service when it is
// down (as after a reboot of the Mac) and brings back the machines that ran
// before, once, however many commands run in parallel. Every call passes
// through the boot lock, so a command that arrives while another one still
// restores the machines waits for them rather than finding them stopped.
func (b *Backend) Ready(ctx context.Context) error {
	release, err := b.bootLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := b.Alive(ctx); err == nil {
		return b.keepHostState(ctx, false)
	}
	api, err := b.lock(ctx)
	if err != nil {
		return err
	}
	defer api()
	b.statusf("==> Starting the container system service\n")
	if _, err := b.runLong(ctx, systemStartArgs...); err != nil {
		return err
	}
	if err := b.restoreMachines(ctx); err != nil {
		b.statusf("  [WARN] the service is running again, but restarting its microVMs failed: %v\n"+
			"         check 'cracklet ls' and start them with 'cracklet start NAME'\n", err)
	}
	return b.keepHostState(ctx, true)
}

// keepHostState runs on every command: it brings the relay back when
// forwards exist without one (after logout, or when it idled out) and
// restores a deleted known_hosts from the records, under the backend lock
// unless the caller holds it (locked), because every writer of known_hosts
// must. Neither fails the command; a problem is reported once per command.
func (b *Backend) keepHostState(ctx context.Context, locked bool) error {
	if _, err := os.Stat(b.paths.KnownHostsPath()); errors.Is(err, fs.ErrNotExist) {
		if err := b.restoreKnownHosts(ctx, locked); err != nil {
			b.statusf("  [WARN] cannot restore %s: %v\n", b.paths.KnownHostsPath(), err)
		}
	}
	if err := b.ensureRelay(ctx); err != nil {
		b.statusf("  [WARN] %v; port forwards stay dead until it starts\n", err)
	}
	return nil
}

func (b *Backend) restoreKnownHosts(ctx context.Context, locked bool) error {
	if !locked {
		release, err := b.lock(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	return b.writeKnownHosts()
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
		return errors.New("host is not ready for cracklet")
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
	// The same path as every other command: when prepare is the first command
	// after a reboot, the machines that were running come back here too.
	if err := b.Ready(ctx); err != nil {
		return err
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
		return errors.New("host is not ready for cracklet")
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
	older, err := olderThan(version, MinVersion)
	if err != nil {
		return err
	}
	if older {
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

// olderThan compares dotted versions numerically, field by field; a
// pre-release suffix ("1.6.0-rc1") is ignored, anything else non-numeric is an
// error rather than a silent zero.
func olderThan(have, want string) (bool, error) {
	parse := func(v string) ([]int, error) {
		base, _, _ := strings.Cut(v, "-")
		var nums []int
		for _, f := range strings.Split(base, ".") {
			n, err := strconv.Atoi(f)
			if err != nil {
				return nil, fmt.Errorf("cannot parse container version %q", v)
			}
			nums = append(nums, n)
		}
		return nums, nil
	}
	h, err := parse(have)
	if err != nil {
		return false, err
	}
	w, err := parse(want)
	if err != nil {
		return false, err
	}
	for i := 0; i < len(h) || i < len(w); i++ {
		var hv, wv int
		if i < len(h) {
			hv = h[i]
		}
		if i < len(w) {
			wv = w[i]
		}
		if hv != wv {
			return hv < wv, nil
		}
	}
	return false, nil
}

func severity(p host.Problem) string {
	if p.Fatal {
		return "ERROR"
	}
	return "WARN"
}
