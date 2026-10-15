package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// record is what cracklet knows about a machine beyond what `container`
// reports: the metadata gc needs and whether the VM should be running (so a
// restart of the service brings it back). The Lima agent keeps the same facts
// inside its VM; here they live on the Mac, next to the backend choice.
type record struct {
	Owner     string     `json:"owner,omitempty"`
	Slot      string     `json:"slot,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	Profile   string     `json:"profile,omitempty"`
	Running   bool       `json:"running"`
	// HostKeys are the "TYPE KEY" lines ssh-keyscan read from the VM on its
	// first boot; known_hosts is rendered from them.
	HostKeys []string `json:"host_keys,omitempty"`
	// Forwards are served by the relay (relay.go); only host and guest port
	// are stored, the state is observed when the VM is described.
	Forwards []vm.Forward `json:"forwards,omitempty"`
}

// errCorruptRecord marks a record file that exists but cannot be parsed; the
// VM is then treated as one without metadata, so it can still be listed and
// removed.
var errCorruptRecord = errors.New("corrupt record")

func (b *Backend) recordPath(name string) string {
	return filepath.Join(b.paths.MachinesDir(), name+".json")
}

// loadRecord returns the record and whether one exists. A corrupt file is
// reported with errCorruptRecord and ok=true.
func (b *Backend) loadRecord(name string) (record, bool, error) {
	data, err := os.ReadFile(b.recordPath(name))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return record{}, false, nil
	case err != nil:
		return record{}, false, fmt.Errorf("read %s: %w", b.recordPath(name), err)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return record{}, true, fmt.Errorf("%s: %w (%v)", b.recordPath(name), errCorruptRecord, err)
	}
	return r, true, nil
}

// saveRecord writes the record atomically: temp file, fsync, rename. Writers
// run under the backend lock, so the fixed temp name cannot collide.
func (b *Backend) saveRecord(name string, r record) error {
	if err := os.MkdirAll(b.paths.MachinesDir(), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", b.paths.MachinesDir(), err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeAtomic(b.recordPath(name), data)
}

// writeAtomic lands data in path without ever exposing a partial file.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

// setRunning records whether the VM should come back after a service restart.
func (b *Backend) setRunning(name string, running bool) error {
	r, ok, err := b.loadRecord(name)
	if err != nil && !errors.Is(err, errCorruptRecord) {
		return err
	}
	if !ok {
		return nil
	}
	r.Running = running
	return b.saveRecord(name, r)
}

// removeRecord drops the record and, with it, the VM's line in known_hosts.
// Callers hold the lock.
func (b *Backend) removeRecord(name string) error {
	if err := os.Remove(b.recordPath(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", b.recordPath(name), err)
	}
	return b.writeKnownHosts()
}

// recordNames lists the VMs cracklet created, sorted.
func (b *Backend) recordNames() ([]string, error) {
	entries, err := os.ReadDir(b.paths.MachinesDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", b.paths.MachinesDir(), err)
	}
	var names []string
	for _, e := range entries {
		if n, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// pruneRecords drops records of machines that no longer exist. It must run
// under the lock: a record is written before its machine appears, so an
// unlocked reader would mistake a VM being created for a vanished one.
func (b *Backend) pruneRecords(machines map[string]machine) error {
	names, err := b.recordNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, ok := machines[name]; !ok {
			if err := b.removeRecord(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// freeName picks the first vm<N> that neither a record nor a machine uses.
func freeName(taken map[string]bool) string {
	for i := 1; ; i++ {
		name := fmt.Sprintf("vm%d", i)
		if !taken[name] {
			return name
		}
	}
}

// lock serializes the backend's mutating `container` calls across processes:
// concurrent machine operations failed in the spike.
func (b *Backend) lock(ctx context.Context) (func(), error) {
	return b.flock(ctx, b.paths.ContainerLockPath())
}

// bootLock is held while the service is started and its machines restored;
// every Ready passes through it, so a command that arrives during a restore
// waits for the VMs instead of finding them stopped.
func (b *Backend) bootLock(ctx context.Context) (func(), error) {
	return b.flock(ctx, b.paths.ContainerBootLockPath())
}

// flock takes an exclusive lock on path, polling so Ctrl-C still works.
func (b *Backend) flock(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(b.paths.Home, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", b.paths.Home, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(b.poll):
		}
	}
}

// infoFrom combines what `container` reports with cracklet's record.
func infoFrom(m machine, r record) vm.Info {
	info := vm.Info{Name: m.ID, IP: m.addr(), State: m.state(), VCPUs: m.CPUs, MemMiB: int(m.Memory >> 20),
		Profile: r.Profile, Forwards: []vm.Forward{}, Owner: r.Owner, Slot: r.Slot, CreatedAt: r.CreatedAt}
	if i := strings.LastIndex(info.IP, "."); i >= 0 {
		_, _ = fmt.Sscanf(info.IP[i+1:], "%d", &info.Index)
	}
	return info
}
