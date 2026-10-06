// Package agent embeds the guest-side script that manages microVMs inside the Lima VM.
package agent

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// Script is the bash agent installed at config.AgentPath inside the Lima VM.
//
//go:embed agent.sh
var Script string

// Checksum returns the SHA-256 of the embedded script, used to detect stale installs.
func Checksum() string {
	sum := sha256.Sum256([]byte(Script))
	return hex.EncodeToString(sum[:])
}
