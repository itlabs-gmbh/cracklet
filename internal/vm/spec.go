// Package vm defines and validates microVM specifications.
package vm

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	// MinMemMiB is the smallest guest memory cracklet accepts.
	MinMemMiB = 128
	// MaxMemMiB caps guest memory (256 GiB) to catch typos before the agent does.
	MaxMemMiB = 262144
	// MaxVCPUs caps the vCPU count per microVM.
	MaxVCPUs = 32
	// MinDisk matches the size of the base root filesystem image; disks can only grow.
	MinDisk = "2G"
	// MaxDisk caps the root disk at 1 TiB.
	MaxDisk       = "1024G"
	maxNameLength = 31
)

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	sizeRe = regexp.MustCompile(`^(\d+)([MmGg])$`)
)

// Spec describes a microVM to create.
type Spec struct {
	// Name is optional; the agent picks "vm<index>" when empty.
	Name   string
	VCPUs  int
	MemMiB int
	// Disk is the root disk size, e.g. "2G" or "512M".
	Disk string
	// Forwards are port forwards to set up after boot, as "[HOST:]GUEST".
	Forwards []string
	// Fresh forces a cold boot instead of restoring the golden snapshot.
	Fresh bool
}

// Mode is the boot mode the agent should use for this spec.
func (s Spec) Mode() string {
	if s.Fresh {
		return "fresh"
	}
	return "snapshot"
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

// ParseSize converts "512M" / "2G" into bytes.
func ParseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("invalid size %q (use e.g. 512M or 2G)", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	shift := uint(30)
	if strings.ToUpper(m[2]) == "M" {
		shift = 20
	}
	if n > math.MaxInt64>>shift {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return n << shift, nil
}

// NormalizeSize returns the canonical form the agent expects: digits plus an
// uppercase unit, with surrounding whitespace removed.
func NormalizeSize(s string) (string, error) {
	if _, err := ParseSize(s); err != nil {
		return "", err
	}
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	return m[1] + strings.ToUpper(m[2]), nil
}

// Validate checks the spec against cracklet limits.
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
	size, err := ParseSize(s.Disk)
	if err != nil {
		return err
	}
	minSize, _ := ParseSize(MinDisk)
	maxSize, _ := ParseSize(MaxDisk)
	if size < minSize || size > maxSize {
		return fmt.Errorf("disk must be between %s (the base image size) and %s, got %s", MinDisk, MaxDisk, s.Disk)
	}
	for _, f := range s.Forwards {
		if _, err := ParseForward(f); err != nil {
			return err
		}
	}
	return nil
}
