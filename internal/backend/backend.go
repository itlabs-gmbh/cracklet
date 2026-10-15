// Package backend is the seam between the cracklet workflows (app) and the
// runtime that actually creates microVMs. It exists to move from Lima with
// nested Firecracker to apple/container; see docs/design/container-backend.md.
// Everything that is the same for every runtime (grants, broker, caps, the
// ssh session) stays in app and talks to the guest over ssh.
package backend

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// PrepareOptions size a shared runtime VM (Lima) and pick the guest image
// profiles to build besides the always-built base image. A new runtime VM
// gets all three sizes; an existing one is resized to the Explicit ones only.
type PrepareOptions struct {
	CPUs      int
	MemoryGiB int
	DiskGiB   int
	Explicit  SizeFlags
	Profiles  []string
}

// SizeFlags marks the sizing options the user set explicitly. Only those are
// applied to an existing runtime VM, so re-running prepare with the defaults
// never undoes an earlier resize.
type SizeFlags struct {
	CPUs, Memory, Disk bool
}

// RemoveGuard makes Remove a compare-and-swap: the VM is only removed if its
// owner and creation time still match, so a VM removed and recreated under
// the same name since the caller listed it survives.
type RemoveGuard struct {
	Owner     string
	CreatedAt *time.Time
}

// Conn is a connection to a guest port. CloseWrite lets a relay signal EOF to
// the guest while it still reads the guest's answer, as a TCP half-close does.
type Conn interface {
	net.Conn
	CloseWrite() error
}

// ErrNoDial is returned by Dial on backends whose guests the Mac cannot reach
// directly; their ssh_config uses another ProxyCommand.
var ErrNoDial = errors.New("direct connections to guests are not supported")

// ForwardRelay is implemented by backends whose port forwards are served on
// the Mac by `cracklet _relay` (a detached child process of the command that
// needed it) rather than inside a runtime VM. ServeForwards listens on every recorded forward until ctx ends or the
// backend decides the relay has nothing left to do.
type ForwardRelay interface {
	ServeForwards(ctx context.Context) error
}

// Capabilities tells app which optional features a backend has, so an
// unsupported flag fails early with a clear message.
type Capabilities struct {
	// Snapshots means --fresh has a meaning (golden snapshots exist).
	Snapshots bool
	// Profiles means a profile sizes the root disk.
	Profiles bool
	// Forwards means `cracklet forward` works.
	Forwards bool
	// GuestFirewall means guests are confined to internet-only.
	GuestFirewall bool
	// Resize means prepare --cpus/--memory/--disk resize a shared runtime VM.
	Resize bool
}

// Backend creates and drives microVMs.
//
// A command calls Ready (or ReadyToBoot) before the VM methods; those do not
// bring the runtime up themselves, so a command issues exactly the probes it
// needs. Ready is idempotent and cheap while the runtime is up, so nested
// workflows (new with --grant) may call it again. Alive is the non-starting
// variant for background polling; Prepare is the one method that sets the
// runtime up from scratch.
type Backend interface {
	// Name is "lima" or "container"; shown by doctor.
	Name() string
	// Capabilities reports the optional features of this backend.
	Capabilities() Capabilities

	// Prepare makes the host ready (idempotent): host check, runtime, images,
	// keys. It validates o before it runs anything.
	Prepare(ctx context.Context, o PrepareOptions) error
	// Doctor prints the host and runtime state without changing anything and
	// fails when the host cannot run cracklet.
	Doctor(ctx context.Context) error
	// Ready brings the runtime up for a foreground command (Lima: start a
	// stopped instance and restore its microVMs) and fails with a hint when
	// Prepare has not run.
	Ready(ctx context.Context) error
	// ReadyToBoot is Ready for a command that is about to create or start a
	// microVM. The caller holds release until the microVM runs; Lima uses it
	// to keep a resize from restarting its VM underneath and fails fast while
	// one is in progress, before anything reaches the runtime.
	ReadyToBoot(ctx context.Context) (release func(), err error)
	// Alive fails unless the runtime is up, without starting it: background
	// polling must not undo a deliberate stop.
	Alive(ctx context.Context) error

	// Create creates and boots a microVM; createdAt is recorded for gc.
	Create(ctx context.Context, spec vm.Spec, createdAt time.Time) (vm.Info, error)
	// Start boots a stopped microVM.
	Start(ctx context.Context, name string) (vm.Info, error)
	// Stop shuts a microVM down but keeps its disk.
	Stop(ctx context.Context, name string) (vm.Info, error)
	// Remove stops and deletes a microVM. With a guard it only removes the VM
	// the caller listed; gone reports that it no longer existed.
	Remove(ctx context.Context, name string, guard *RemoveGuard) (gone bool, err error)
	// List returns every microVM the backend knows, without grants.
	List(ctx context.Context) ([]vm.Info, error)

	// Forward adds port forwards; they persist across stop/start.
	Forward(ctx context.Context, name string, forwards []vm.Forward) (vm.Info, error)
	// Unforward removes forwards by host port.
	Unforward(ctx context.Context, name string, hostPorts []int) (vm.Info, error)

	// SSHProxyCommand is the ProxyCommand line rendered into ssh_config, with
	// ssh's %h and %p placeholders (e.g. "cracklet _dial %h %p").
	SSHProxyCommand() string
	// KnownHostsFile is the file with the pinned host key of every microVM,
	// rendered into ssh_config with strict host key checking; empty when the
	// backend does not pin keys.
	KnownHostsFile() string
	// Dial opens a TCP connection to port inside the named VM. It backs
	// `cracklet _dial` and the forward relay; backends without a direct path
	// return ErrNoDial.
	Dial(ctx context.Context, name string, port int) (Conn, error)
}
