// Package host gathers facts about the Mac and the problems that keep it
// from running skiff; the backend decides which facts matter.
package host

import (
	"fmt"
	"strconv"
	"strings"
)

// Facts captures what the preflight check needs to know about the host.
type Facts struct {
	OS           string
	Arch         string
	MacOSVersion string
	Chip         string
	HVSupport    bool
	HasBrew      bool
	// HasContainer reports Apple's `container` CLI on PATH.
	HasContainer bool
}

// Problem is a single failed preflight check.
type Problem struct {
	Code    string
	Message string
	Fatal   bool
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

// HasFatal reports whether any problem blocks further work.
func HasFatal(problems []Problem) bool {
	for _, p := range problems {
		if p.Fatal {
			return true
		}
	}
	return false
}
