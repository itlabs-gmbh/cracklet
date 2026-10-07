package grant

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
)

// Store keeps per-VM grants and placeholder tokens on the Mac, where the
// broker runs, so policy and enforcement live in the same place.
type Store struct {
	// Dir is the root, usually ~/.cracklet/vms.
	Dir string
}

const (
	grantsFile = "grants"
	tokenFile  = "token"
	tokenBytes = 16
)

// vmDir validates the name before building a path; VM names and cap names
// share the same label rules.
func (s Store) vmDir(vm string) (string, error) {
	if err := cap.ValidateName(vm); err != nil {
		return "", fmt.Errorf("vm %w", err)
	}
	return filepath.Join(s.Dir, vm), nil
}

// Load returns the grants of a VM; a VM without state has none.
func (s Store) Load(vm string) (Set, error) {
	dir, err := s.vmDir(vm)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, grantsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read grants: %w", err)
	}
	var specs []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			specs = append(specs, line)
		}
	}
	set, err := ParseSet(specs)
	if err != nil {
		return nil, fmt.Errorf("grants of %s: %w", vm, err)
	}
	return set, nil
}

// Save replaces the grants of a VM.
func (s Store) Save(vm string, set Set) error {
	dir, err := s.vmDir(vm)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create vm state: %w", err)
	}
	text := strings.Join(set.Strings(), "\n")
	if text != "" {
		text += "\n"
	}
	return writeAtomic(filepath.Join(dir, grantsFile), []byte(text), 0o600)
}

// Token returns the VM's placeholder token, creating it on first use. The
// broker never verifies it; it exists for clients that insist on a token.
func (s Store) Token(vm string) (string, error) {
	dir, err := s.vmDir(vm)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, tokenFile)
	data, err := os.ReadFile(path)
	if err == nil && len(strings.TrimSpace(string(data))) > 0 {
		return strings.TrimSpace(string(data)), nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read token: %w", err)
	}
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(raw)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create vm state: %w", err)
	}
	if err := writeAtomic(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

// Lock takes an exclusive, cross-process lock for a VM and returns the
// function that releases it. Grant and revoke hold it from reading the
// grants through provisioning the guest to saving, so two invocations
// cannot lose each other's change (atomic file replacement alone would let
// a slower grant re-save a grant that a concurrent revoke removed).
func (s Store) Lock(vm string) (func(), error) {
	dir, err := s.vmDir(vm)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create vm state: %w", err)
	}
	f, err := os.OpenFile(dir+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", vm, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// Remove deletes all state of a VM. The lock file itself stays: deleting it
// would let a later Lock create a fresh file and succeed while a holder of
// the old inode still believes it is exclusive.
func (s Store) Remove(vm string) error {
	unlock, err := s.Lock(vm)
	if err != nil {
		return err
	}
	defer unlock()
	dir, err := s.vmDir(vm)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove vm state: %w", err)
	}
	return nil
}

// writeAtomic writes through a uniquely named temp file so two concurrent
// writers cannot clobber each other's temp file; the last rename wins whole.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
