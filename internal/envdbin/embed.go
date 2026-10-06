// Package envdbin embeds the statically linked linux/arm64 build of cracklet-envd,
// produced by `make envd`, so that cracklet can ship it into the Lima VM.
package envdbin

import _ "embed"

// Binary is the cracklet-envd executable for the guest.
//
//go:embed cracklet-envd
var Binary []byte
