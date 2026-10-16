// Package vm defines and validates VM specifications.
package vm

import (
	"fmt"
	"regexp"
)

const (
	// MinMemMiB is the smallest guest memory Apple's container accepts.
	MinMemMiB = 1024
	// MaxMemMiB caps guest memory (256 GiB) to catch typos early.
	MaxMemMiB = 262144
	// MaxVCPUs caps the vCPU count per VM.
	MaxVCPUs       = 32
	maxNameLength  = 31
	maxLabelLength = 63
)

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// labelRe keeps owner and slot labels to characters that need no
	// quoting anywhere they are rendered.
	labelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Spec describes a VM to create.
type Spec struct {
	// Name is optional; the backend picks "vm<index>" when empty.
	Name   string
	VCPUs  int
	MemMiB int
	// Forwards are port forwards to set up after boot, as "[HOST:]GUEST".
	Forwards []string
	// Grants are capabilities to grant right after creation, by capability name.
	Grants []string
	// Owner labels the tool or person that manages the VM; only owned VMs
	// are candidates for `skiff gc`. Empty means created by hand.
	Owner string
	// Slot is the VM's position in its owner's pool, e.g. "3"; needs Owner.
	Slot string
}

// ValidateName enforces DNS-label-like names so they are safe in hostnames, unit names and paths.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if len(name) > maxNameLength {
		return fmt.Errorf("name %q is longer than %d characters", name, maxNameLength)
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("name %q must start with a letter and contain only lowercase letters, digits and dashes", name)
	}
	return nil
}

// ValidateLabel checks an owner or slot label; kind names it in errors.
func ValidateLabel(kind, label string) error {
	if len(label) > maxLabelLength {
		return fmt.Errorf("%s %q is longer than %d characters", kind, label, maxLabelLength)
	}
	if !labelRe.MatchString(label) {
		return fmt.Errorf("%s %q must start with a letter or digit and contain only letters, digits, '.', '_' and '-'", kind, label)
	}
	return nil
}

// Validate checks the spec against skiff limits.
func (s Spec) Validate() error {
	if s.Name != "" {
		if err := ValidateName(s.Name); err != nil {
			return err
		}
	}
	if s.VCPUs < 1 || s.VCPUs > MaxVCPUs {
		return fmt.Errorf("vcpus must be between 1 and %d, got %d", MaxVCPUs, s.VCPUs)
	}
	if s.MemMiB < MinMemMiB || s.MemMiB > MaxMemMiB {
		return fmt.Errorf("memory must be between %d and %d MiB, got %d", MinMemMiB, MaxMemMiB, s.MemMiB)
	}
	for _, f := range s.Forwards {
		if _, err := ParseForward(f); err != nil {
			return err
		}
	}
	return s.validateMetadata()
}

func (s Spec) validateMetadata() error {
	if s.Owner != "" {
		if err := ValidateLabel("owner", s.Owner); err != nil {
			return err
		}
	}
	if s.Slot == "" {
		return nil
	}
	if s.Owner == "" {
		return fmt.Errorf("slot %q needs an owner", s.Slot)
	}
	return ValidateLabel("slot", s.Slot)
}
