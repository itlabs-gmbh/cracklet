package backend

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/config"
)

// Backend names as stored in ~/.cracklet/backend and given to --backend.
const (
	NameLima      = "lima"
	NameContainer = "container"
	// DefaultName is used while no choice has been stored. It flips to
	// NameContainer in phase 5 of docs/design/container-backend.md.
	DefaultName = NameLima
)

// Names lists the selectable backends.
func Names() []string { return []string{NameLima, NameContainer} }

// ValidateName accepts a backend name from the command line.
func ValidateName(name string) error {
	for _, n := range Names() {
		if n == name {
			return nil
		}
	}
	return fmt.Errorf("unknown backend %q (use %s)", name, strings.Join(Names(), " or "))
}

// LoadName returns the stored backend choice, or DefaultName when none is stored.
func LoadName(paths config.Paths) (string, error) {
	data, err := os.ReadFile(paths.BackendPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return DefaultName, nil
	case err != nil:
		return "", fmt.Errorf("read %s: %w", paths.BackendPath(), err)
	}
	name := strings.TrimSpace(string(data))
	if err := ValidateName(name); err != nil {
		return "", fmt.Errorf("%s: %w", paths.BackendPath(), err)
	}
	return name, nil
}

// SaveName stores the backend choice atomically, so a concurrent LoadName
// never sees a truncated file. A CRACKLET_HOME has one backend at a time:
// switching does not migrate VMs, the other backend's VMs simply stop being
// listed until the choice is switched back (callers warn about that).
func SaveName(paths config.Paths, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", paths.Home, err)
	}
	tmp, err := os.CreateTemp(paths.Home, "backend-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", paths.BackendPath(), err)
	}
	if _, err := tmp.WriteString(name + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), paths.BackendPath()); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", paths.BackendPath(), err)
	}
	return nil
}
