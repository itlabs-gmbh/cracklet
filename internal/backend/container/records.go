package container

import (
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

	"context"

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
}

func (b *Backend) recordPath(name string) string {
	return filepath.Join(b.paths.MachinesDir(), name+".json")
}

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
		return record{}, false, fmt.Errorf("parse %s: %w", b.recordPath(name), err)
	}
	return r, true, nil
}

// saveRecord writes the record atomically (temp file + rename).
func (b *Backend) saveRecord(name string, r record) error {
	if err := os.MkdirAll(b.paths.MachinesDir(), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", b.paths.MachinesDir(), err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := b.recordPath(name) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, b.recordPath(name)); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

func (b *Backend) setRunning(name string, running bool) error {
	r, ok, err := b.loadRecord(name)
	if err != nil || !ok {
		return err
	}
	r.Running = running
	return b.saveRecord(name, r)
}

func (b *Backend) removeRecord(name string) error {
	if err := os.Remove(b.recordPath(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", b.recordPath(name), err)
	}
	return nil
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
// concurrent machine operations failed in the spike. It waits, polling so
// Ctrl-C still works.
func (b *Backend) lock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(b.paths.Home, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", b.paths.Home, err)
	}
	f, err := os.OpenFile(b.paths.ContainerLockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", b.paths.ContainerLockPath(), err)
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
			return nil, fmt.Errorf("lock %s: %w", b.paths.ContainerLockPath(), err)
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
	info := vm.Info{Name: m.ID, IP: m.IP, State: m.state(), VCPUs: m.CPUs, MemMiB: int(m.Memory >> 20),
		Profile: r.Profile, Forwards: []vm.Forward{}, Owner: r.Owner, Slot: r.Slot, CreatedAt: r.CreatedAt}
	if i := strings.LastIndex(m.IP, "."); i >= 0 {
		fmt.Sscanf(m.IP[i+1:], "%d", &info.Index)
	}
	return info
}
