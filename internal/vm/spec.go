// Package vm defines and validates microVM specifications.
package vm

import (
	"fmt"
	"math"
	"regexp"
	"sort"
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
	// MaxDisk caps the root disk at 1 TiB.
	MaxDisk        = "1024G"
	maxNameLength  = 31
	maxLabelLength = 63

	// ProfileBase is the plain Ubuntu guest image.
	ProfileBase = "base"
	// ProfilePaseo adds Node 22, the Paseo CLI, Claude Code, git and gh.
	ProfilePaseo = "paseo"
)

// profileDisks is the size each profile image is built with. It is the
// smallest disk a VM of that profile can have (disks only grow) and the only
// size the golden snapshot can be restored with. Must match the agent.
var profileDisks = map[string]string{
	ProfileBase:  "2G",
	ProfilePaseo: "8G",
}

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	// labelRe keeps owner and slot labels to characters that need no
	// quoting: they reach the agent as plain words through `limactl shell`.
	labelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	sizeRe  = regexp.MustCompile(`^(\d+)([MmGg])$`)
)

// Spec describes a microVM to create.
type Spec struct {
	// Name is optional; the agent picks "vm<index>" when empty.
	Name   string
	VCPUs  int
	MemMiB int
	// Disk is the root disk size, e.g. "2G" or "512M"; empty means the
	// profile's image size.
	Disk string
	// Profile selects the guest image; empty means ProfileBase.
	Profile string
	// Forwards are port forwards to set up after boot, as "[HOST:]GUEST".
	Forwards []string
	// Fresh forces a cold boot instead of restoring the golden snapshot.
	Fresh bool
	// Grants are capabilities to grant right after creation, as "cap[:scope]".
	Grants []string
	// Owner labels the tool or person that manages the VM; only owned VMs
	// are candidates for `cracklet gc`. Empty means created by hand.
	Owner string
	// Slot is the VM's position in its owner's pool, e.g. "3"; needs Owner.
	Slot string
}

// Mode is the boot mode the agent should use for this spec.
func (s Spec) Mode() string {
	if s.Fresh {
		return "fresh"
	}
	return "snapshot"
}

// ProfileName is the profile the agent should use for this spec.
func (s Spec) ProfileName() string {
	if s.Profile == "" {
		return ProfileBase
	}
	return s.Profile
}

// DiskSize is the requested disk, defaulting to the profile's image size so
// that the VM can be restored from the golden snapshot.
func (s Spec) DiskSize() string {
	if s.Disk != "" {
		return s.Disk
	}
	disk, _ := ProfileDisk(s.ProfileName()) // unknown profiles fail in Validate
	return disk
}

// ProfileNames lists the known guest image profiles.
func ProfileNames() []string {
	names := make([]string, 0, len(profileDisks))
	for name := range profileDisks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateProfile rejects profiles the agent cannot build.
func ValidateProfile(name string) error {
	if _, ok := profileDisks[name]; !ok {
		return fmt.Errorf("unknown profile %q (available: %s)", name, strings.Join(ProfileNames(), ", "))
	}
	return nil
}

// ProfileDisk is the image size of a profile; empty means ProfileBase.
func ProfileDisk(name string) (string, error) {
	if name == "" {
		name = ProfileBase
	}
	if err := ValidateProfile(name); err != nil {
		return "", err
	}
	return profileDisks[name], nil
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
	minDisk, err := ProfileDisk(s.ProfileName())
	if err != nil {
		return err
	}
	size, err := ParseSize(s.DiskSize())
	if err != nil {
		return err
	}
	minSize, _ := ParseSize(minDisk)
	maxSize, _ := ParseSize(MaxDisk)
	if size < minSize || size > maxSize {
		return fmt.Errorf("disk must be between %s (the %s image size) and %s, got %s",
			minDisk, s.ProfileName(), MaxDisk, s.DiskSize())
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
