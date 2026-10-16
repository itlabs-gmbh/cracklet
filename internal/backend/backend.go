// Package backend is the seam between the skiff workflows (app) and the
// runtime that creates the VMs: Apple's container CLI (backend/container).
// Everything that is the same for every runtime (grants, broker, caps, the
// ssh session) stays in app and talks to the guest over ssh. The interface
// remains so app can be tested against a fake.
package backend

import (
	"context"
	"net"
	"time"

	"github.com/itlabs-gmbh/skiff/internal/vm"
)

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

// ForwardRelay is implemented by backends whose port forwards are served on
// the Mac by `skiff _relay` (a detached child process of the command that
// needed it) rather than inside a runtime VM. ServeForwards listens on every
// recorded forward until ctx ends or the backend decides the relay has
// nothing left to do.
type ForwardRelay interface {
	ServeForwards(ctx context.Context) error
}

// Backend creates and drives VMs.
//
// A command calls Ready before the VM methods; those do not bring the runtime
// up themselves, so a command issues exactly the probes it needs. Ready is
// idempotent and cheap while the runtime is up, so nested workflows (new with
// --grant) may call it again. Alive is the non-starting variant for
// background polling; Prepare is the one method that sets the runtime up
// from scratch.
type Backend interface {
	// Name is shown by doctor.
	Name() string

	// Prepare makes the host ready (idempotent): host check, runtime, image,
	// keys.
	Prepare(ctx context.Context) error
	// Doctor prints the host and runtime state without changing anything and
	// fails when the host cannot run skiff.
	Doctor(ctx context.Context) error
	// Ready brings the runtime up for a foreground command and fails with a
	// hint when Prepare has not run.
	Ready(ctx context.Context) error
	// Alive fails unless the runtime is up, without starting it: background
	// polling must not undo a deliberate stop.
	Alive(ctx context.Context) error

	// Create creates and boots a VM; createdAt is recorded for gc.
	Create(ctx context.Context, spec vm.Spec, createdAt time.Time) (vm.Info, error)
	// Start boots a stopped VM.
	Start(ctx context.Context, name string) (vm.Info, error)
	// Stop shuts a VM down but keeps its disk.
	Stop(ctx context.Context, name string) (vm.Info, error)
	// Remove stops and deletes a VM. With a guard it only removes the VM the
	// caller listed; gone reports that it no longer existed.
	Remove(ctx context.Context, name string, guard *RemoveGuard) (gone bool, err error)
	// List returns every VM the backend knows, without grants.
	List(ctx context.Context) ([]vm.Info, error)

	// Forward adds port forwards; they persist across stop/start.
	Forward(ctx context.Context, name string, forwards []vm.Forward) (vm.Info, error)
	// Unforward removes forwards by host port.
	Unforward(ctx context.Context, name string, hostPorts []int) (vm.Info, error)

	// SSHProxyCommand is the ProxyCommand line rendered into ssh_config, with
	// ssh's %h and %p placeholders (e.g. "skiff _dial %h %p").
	SSHProxyCommand() string
	// KnownHostsFile is the file with the pinned host key of every VM,
	// rendered into ssh_config with strict host key checking.
	KnownHostsFile() string
	// Dial opens a TCP connection to port inside the named VM. It backs
	// `skiff _dial` and the forward relay.
	Dial(ctx context.Context, name string, port int) (Conn, error)
}
