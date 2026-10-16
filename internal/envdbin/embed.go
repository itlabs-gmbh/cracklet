// Package envdbin provides the statically linked linux/arm64 build of skiff-envd
// that skiff ships into the Lima VM. `make envd` embeds it at compile time; a
// skiff installed with `go install ...@latest` has no embedded copy and
// cross-compiles it on demand with the local Go toolchain instead (see Builder).
package envdbin

import "embed"

// MinSize is the smallest byte count a real daemon binary can have; anything
// below it is a placeholder or a truncated build.
const MinSize = 1024

// embeddedPath is where `make envd` drops the daemon inside this package.
const embeddedPath = "bin/skiff-envd"

// The directory holds only .gitkeep in a clean checkout. The all: prefix makes
// the pattern match that dotfile, so the package compiles without the binary.
//
//go:embed all:bin
var files embed.FS

// Embedded returns the daemon compiled into this skiff, or ok=false when
// skiff was built without `make envd`.
func Embedded() (data []byte, ok bool) {
	data, err := files.ReadFile(embeddedPath)
	if err != nil || len(data) < MinSize {
		return nil, false
	}
	return data, true
}
