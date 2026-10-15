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

// SaveName stores the backend choice. A CRACKLET_HOME has one backend; the
// caller refuses a switch while the other backend still owns VMs.
func SaveName(paths config.Paths, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", paths.Home, err)
	}
	if err := os.WriteFile(paths.BackendPath(), []byte(name+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", paths.BackendPath(), err)
	}
	return nil
}
