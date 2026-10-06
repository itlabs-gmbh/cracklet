// Package host checks that the macOS host can run nested virtualization.
package host

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	// MinMacOSMajor is the first macOS release exposing nested virtualization.
	MinMacOSMajor = 15
	// MinChipGeneration is the first Apple Silicon generation with nested virtualization.
	MinChipGeneration = 3
)

// Facts captures everything the preflight check needs to know about the host.
type Facts struct {
	OS           string
	Arch         string
	MacOSVersion string
	Chip         string
	HVSupport    bool
	HasBrew      bool
	HasLima      bool
}

// Problem is a single failed preflight check.
type Problem struct {
	Code    string
	Message string
	Fatal   bool
}

var chipRe = regexp.MustCompile(`Apple M(\d+)`)

// ChipGeneration extracts the Apple Silicon generation from a CPU brand string.
func ChipGeneration(brand string) (int, bool) {
	m := chipRe.FindStringSubmatch(brand)
	if m == nil {
		return 0, false
	}
	gen, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return gen, true
}

// MajorVersion parses the leading number of a dotted version string.
func MajorVersion(v string) (int, error) {
	first, _, _ := strings.Cut(strings.TrimSpace(v), ".")
	major, err := strconv.Atoi(first)
	if err != nil {
		return 0, fmt.Errorf("invalid version %q", v)
	}
	return major, nil
}

// Check evaluates facts and returns every problem found.
func Check(f Facts) []Problem {
	if f.OS != "darwin" || f.Arch != "arm64" {
		return []Problem{{
			Code:    "platform",
			Message: fmt.Sprintf("cracklet needs macOS on Apple Silicon, found %s/%s", f.OS, f.Arch),
			Fatal:   true,
		}}
	}
	var problems []Problem
	if major, err := MajorVersion(f.MacOSVersion); err != nil || major < MinMacOSMajor {
		problems = append(problems, Problem{
			Code:    "macos-version",
			Message: fmt.Sprintf("macOS %d or newer is required for nested virtualization, found %q", MinMacOSMajor, f.MacOSVersion),
			Fatal:   true,
		})
	}
	if gen, ok := ChipGeneration(f.Chip); !ok || gen < MinChipGeneration {
		problems = append(problems, Problem{
			Code:    "chip",
			Message: fmt.Sprintf("an Apple M%d chip or newer is required for nested virtualization, found %q", MinChipGeneration, f.Chip),
			Fatal:   true,
		})
	}
	if !f.HVSupport {
		problems = append(problems, Problem{
			Code:    "hv-support",
			Message: "the Hypervisor framework is not available (kern.hv_support != 1)",
			Fatal:   true,
		})
	}
	if !f.HasLima {
		p := Problem{Code: "lima-missing", Message: "limactl is not installed", Fatal: !f.HasBrew}
		if f.HasBrew {
			p.Message += "; it will be installed with 'brew install lima'"
		} else {
			p.Message += " and Homebrew is unavailable to install it (https://lima-vm.io)"
		}
		problems = append(problems, p)
	}
	return problems
}

// HasFatal reports whether any problem blocks further work.
func HasFatal(problems []Problem) bool {
	for _, p := range problems {
		if p.Fatal {
			return true
		}
	}
	return false
}
