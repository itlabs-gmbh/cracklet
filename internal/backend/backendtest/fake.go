// Package backendtest provides an in-memory backend.Backend for tests of the
// workflows in app and the commands in cli: VMs live in a map, every call is
// recorded as one line, and hooks inject failures where a test needs them.
package backendtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/vm"
)

// Fake implements backend.Backend in memory.
type Fake struct {
	mu   sync.Mutex
	vms  []*vm.Info
	next int
	// Calls lists every invocation, e.g. "create dev 2 1024", "rm dev",
	// "forward dev 8080:80", "list", "ready".
	Calls []string
	// ReadyErr, AliveErr, PrepareErr and ListErr fail the respective call.
	ReadyErr, AliveErr, PrepareErr, ListErr error
	// OnCreate runs before a VM is created; an error aborts the creation.
	OnCreate func(ctx context.Context, spec vm.Spec) error
	// OnRemove runs before a VM is removed; an error aborts the removal.
	OnRemove func(name string) error
	// OnRemoved runs after a VM was removed (a concurrent `new` of the same
	// name can seed it again here).
	OnRemoved func(name string)
	// OnList runs on every List before the VMs are read; an error fails it.
	OnList func() error
	// Dialer serves Dial; nil means every Dial fails.
	Dialer func(ctx context.Context, name string, port int) (backend.Conn, error)
	// KnownHosts is what KnownHostsFile returns.
	KnownHosts string
	// Doctor writes its report to Out when set.
	Out io.Writer
}

var _ backend.Backend = (*Fake)(nil)

// New returns an empty, ready Fake.
func New() *Fake {
	return &Fake{KnownHosts: "/tmp/known_hosts"}
}

// Add seeds a VM as the backend would report it; a missing State means
// running, a missing Index/IP are derived from the position.
func (f *Fake) Add(info vm.Info) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	if info.Index == 0 {
		info.Index = f.next
	}
	if info.IP == "" && info.State != vm.StateStopped {
		info.IP = fmt.Sprintf("192.168.64.%d", info.Index+1)
	}
	if info.State == "" {
		info.State = vm.StateRunning
	}
	if info.Forwards == nil {
		info.Forwards = []vm.Forward{}
	}
	f.vms = append(f.vms, &info)
}

// VM returns a copy of the named VM.
func (f *Fake) VM(name string) (vm.Info, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v := f.find(name); v != nil {
		return *v, true
	}
	return vm.Info{}, false
}

// Drop forgets a VM without recording a call, as if another process had
// removed it.
func (f *Fake) Drop(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, v := range f.vms {
		if v.Name == name {
			f.vms = append(f.vms[:i], f.vms[i+1:]...)
			return
		}
	}
}

// SetState changes a VM's state behind the backend's back.
func (f *Fake) SetState(name, state string) {
	_, _ = f.setState(name, state)
}

// Names lists the VMs in creation order.
func (f *Fake) Names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.vms))
	for _, v := range f.vms {
		names = append(names, v.Name)
	}
	return names
}

// Called reports whether a call with exactly this line was recorded.
func (f *Fake) Called(line string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if c == line {
			return true
		}
	}
	return false
}

// CalledWithPrefix reports whether a call starting with prefix was recorded.
func (f *Fake) CalledWithPrefix(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// Dump renders the recorded calls for a failure message.
func (f *Fake) Dump() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.Calls, "\n")
}

func (f *Fake) record(line string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, line)
}

func (f *Fake) find(name string) *vm.Info {
	for _, v := range f.vms {
		if v.Name == name {
			return v
		}
	}
	return nil
}

// Name implements backend.Backend.
func (f *Fake) Name() string { return "fake" }

// Prepare implements backend.Backend.
func (f *Fake) Prepare(context.Context) error {
	f.record("prepare")
	return f.PrepareErr
}

// Doctor implements backend.Backend.
func (f *Fake) Doctor(context.Context) error {
	f.record("doctor")
	if f.Out != nil {
		fmt.Fprintln(f.Out, "fake backend: ok")
	}
	return nil
}

// Ready implements backend.Backend.
func (f *Fake) Ready(context.Context) error {
	f.record("ready")
	return f.ReadyErr
}

// Alive implements backend.Backend.
func (f *Fake) Alive(context.Context) error {
	f.record("alive")
	return f.AliveErr
}

// Create implements backend.Backend: the VM runs at once under the given or
// the next free vm<N> name.
func (f *Fake) Create(ctx context.Context, spec vm.Spec, createdAt time.Time) (vm.Info, error) {
	line := fmt.Sprintf("create %s %d %d", spec.Name, spec.VCPUs, spec.MemMiB)
	if spec.Owner != "" {
		line += " " + spec.Owner + " " + spec.Slot
	}
	f.record(line)
	if f.OnCreate != nil {
		if err := f.OnCreate(ctx, spec); err != nil {
			return vm.Info{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	name := spec.Name
	if name == "" {
		for i := 1; ; i++ {
			name = "vm" + strconv.Itoa(i)
			if f.find(name) == nil {
				break
			}
		}
	} else if f.find(name) != nil {
		return vm.Info{}, fmt.Errorf("VM '%s' already exists", name)
	}
	f.next++
	info := &vm.Info{Name: name, Index: f.next, IP: fmt.Sprintf("192.168.64.%d", f.next+1), State: vm.StateRunning,
		VCPUs: spec.VCPUs, MemMiB: spec.MemMiB, Forwards: []vm.Forward{}, Owner: spec.Owner, Slot: spec.Slot}
	if spec.Owner != "" {
		created := createdAt.UTC()
		info.CreatedAt = &created
	}
	f.vms = append(f.vms, info)
	return *info, nil
}

// Start implements backend.Backend.
func (f *Fake) Start(_ context.Context, name string) (vm.Info, error) {
	f.record("start " + name)
	return f.setState(name, vm.StateRunning)
}

// Stop implements backend.Backend.
func (f *Fake) Stop(_ context.Context, name string) (vm.Info, error) {
	f.record("stop " + name)
	return f.setState(name, vm.StateStopped)
}

func (f *Fake) setState(name, state string) (vm.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.find(name)
	if v == nil {
		return vm.Info{}, fmt.Errorf("VM '%s' does not exist", name)
	}
	v.State = state
	for i := range v.Forwards {
		v.Forwards[i].State = forwardState(state)
	}
	return *v, nil
}

func forwardState(state string) string {
	if state == vm.StateRunning {
		return "active"
	}
	return "inactive"
}

// Remove implements backend.Backend with the guarded semantics of the real
// backend: a missing VM is an error without a guard and "gone" with one; a
// guard that does not match owner and creation time leaves the VM alone and
// reports gone.
func (f *Fake) Remove(_ context.Context, name string, guard *backend.RemoveGuard) (bool, error) {
	line := "rm " + name
	if guard != nil {
		created := "-"
		if guard.CreatedAt != nil {
			created = guard.CreatedAt.UTC().Format(time.RFC3339)
		}
		line += " " + guard.Owner + " " + created
	}
	f.record(line)
	if f.OnRemove != nil {
		if err := f.OnRemove(name); err != nil {
			return false, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, v := range f.vms {
		if v.Name != name {
			continue
		}
		if guard != nil && (v.Owner != guard.Owner || !sameTime(v.CreatedAt, guard.CreatedAt)) {
			return true, nil
		}
		f.vms = append(f.vms[:i], f.vms[i+1:]...)
		if f.OnRemoved != nil {
			f.mu.Unlock()
			f.OnRemoved(name)
			f.mu.Lock()
		}
		return false, nil
	}
	if guard != nil {
		return true, nil
	}
	return false, fmt.Errorf("VM '%s' does not exist", name)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// List implements backend.Backend.
func (f *Fake) List(context.Context) ([]vm.Info, error) {
	f.record("list")
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	if f.OnList != nil {
		if err := f.OnList(); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]vm.Info, 0, len(f.vms))
	for _, v := range f.vms {
		c := *v
		c.Forwards = append([]vm.Forward{}, v.Forwards...)
		out = append(out, c)
	}
	return out, nil
}

// Forward implements backend.Backend: a host port owned by another VM is
// refused, one owned by the same VM is retargeted.
func (f *Fake) Forward(_ context.Context, name string, forwards []vm.Forward) (vm.Info, error) {
	specs := make([]string, 0, len(forwards))
	for _, fw := range forwards {
		specs = append(specs, fw.Spec())
	}
	f.record("forward " + name + " " + strings.Join(specs, " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.find(name)
	if v == nil {
		return vm.Info{}, fmt.Errorf("VM '%s' does not exist", name)
	}
	for _, fw := range forwards {
		for _, other := range f.vms {
			if other.Name == name {
				continue
			}
			for _, o := range other.Forwards {
				if o.Host == fw.Host {
					return vm.Info{}, fmt.Errorf("host port %d is already forwarded to VM '%s'", fw.Host, other.Name)
				}
			}
		}
	}
	for _, fw := range forwards {
		entry := vm.Forward{Host: fw.Host, Guest: fw.Guest, State: forwardState(v.State)}
		replaced := false
		for i := range v.Forwards {
			if v.Forwards[i].Host == fw.Host {
				v.Forwards[i] = entry
				replaced = true
			}
		}
		if !replaced {
			v.Forwards = append(v.Forwards, entry)
		}
	}
	return *v, nil
}

// Unforward implements backend.Backend.
func (f *Fake) Unforward(_ context.Context, name string, hostPorts []int) (vm.Info, error) {
	ports := make([]string, 0, len(hostPorts))
	for _, p := range hostPorts {
		ports = append(ports, strconv.Itoa(p))
	}
	f.record("unforward " + name + " " + strings.Join(ports, " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.find(name)
	if v == nil {
		return vm.Info{}, fmt.Errorf("VM '%s' does not exist", name)
	}
	kept := v.Forwards[:0:0]
	for _, fw := range v.Forwards {
		drop := false
		for _, p := range hostPorts {
			if fw.Host == p {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, fw)
		}
	}
	v.Forwards = kept
	return *v, nil
}

// SSHProxyCommand implements backend.Backend.
func (f *Fake) SSHProxyCommand() string { return "/usr/local/bin/skiff _dial %h %p" }

// KnownHostsFile implements backend.Backend.
func (f *Fake) KnownHostsFile() string { return f.KnownHosts }

// Dial implements backend.Backend.
func (f *Fake) Dial(ctx context.Context, name string, port int) (backend.Conn, error) {
	f.record(fmt.Sprintf("dial %s %d", name, port))
	if f.Dialer == nil {
		return nil, errors.New("fake backend: no dialer")
	}
	return f.Dialer(ctx, name, port)
}
