// Package config holds the pinned versions, URLs and filesystem layout used by cracklet.
package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// Instance is the name of the Lima VM that hosts all microVMs.
	Instance = "cracklet"

	// FirecrackerVersion is the upstream release installed inside the Lima VM.
	FirecrackerVersion = "v1.17.0"

	// artifactBase is a dated Firecracker CI artifact set (kernel + rootfs) for aarch64.
	artifactBase = "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260930-a738f18a8db0-0/aarch64"
	// KernelURL points at an uncompressed Linux 6.1 LTS guest kernel built for Firecracker.
	KernelURL = artifactBase + "/vmlinux-6.1.186"
	// RootfsURL points at the Ubuntu 24.04 squashfs used to build the base root filesystem.
	RootfsURL = artifactBase + "/ubuntu-24.04.squashfs"

	// FirecrackerSHA256 is the published digest of the release tarball.
	FirecrackerSHA256 = "e351ebe4f7a16b5873bbd51005d2e6767103cff4d5ebc829df2d3f95a93e2256"
	// KernelSHA256 and RootfsSHA256 pin the CI artifacts (no digests are published
	// for them; these were recorded from two independent downloads).
	KernelSHA256 = "a4d2441d2c31116fcfc80c0e955ab3f46e131a3bd6a95a2c69ba66d4a7b6ccde"
	RootfsSHA256 = "78f88484ab3f88b7b4b71937e00b6dda8b3ea17bfeb0ef8797b751d78ebb52dc"

	// AgentPath is where the guest-side agent script lives inside the Lima VM.
	AgentPath = "/usr/local/lib/cracklet/agent.sh"
	// GuestKeyPath is where the SSH private key is stored inside the Lima VM.
	GuestKeyPath = "/var/lib/cracklet/id_ed25519"
	// GuestEnvdPath is where the embedded guest daemon is placed for the agent.
	GuestEnvdPath = "/var/lib/cracklet/cracklet-envd"

	DefaultLimaCPUs      = 4
	DefaultLimaMemoryGiB = 8
	DefaultLimaDiskGiB   = 40

	DefaultVCPUs  = 2
	DefaultMemMiB = 1024
	DefaultDisk   = "2G"
)

// Paths describes the host-side directories cracklet works with.
type Paths struct {
	// Home is the cracklet state directory (default ~/.cracklet).
	Home string
	// LimaHome is Lima's state directory (default ~/.lima).
	LimaHome string
}

// DefaultPaths resolves Paths from the environment (CRACKLET_HOME, LIMA_HOME) or the user's home.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	p := Paths{
		Home:     filepath.Join(home, ".cracklet"),
		LimaHome: filepath.Join(home, ".lima"),
	}
	if v := os.Getenv("CRACKLET_HOME"); v != "" {
		p.Home = v
	}
	if v := os.Getenv("LIMA_HOME"); v != "" {
		p.LimaHome = v
	}
	return p, nil
}

// KeyPath is the SSH private key used to reach microVMs.
func (p Paths) KeyPath() string { return filepath.Join(p.Home, "id_ed25519") }

// PubKeyPath is the matching public key.
func (p Paths) PubKeyPath() string { return p.KeyPath() + ".pub" }

// SSHConfigPath is the generated ssh_config with the *.cracklet host alias.
func (p Paths) SSHConfigPath() string { return filepath.Join(p.Home, "ssh_config") }

// LimaTemplatePath is the rendered Lima template used to create the instance.
func (p Paths) LimaTemplatePath() string { return filepath.Join(p.Home, "lima.yaml") }

// LimaSSHConfig is the ssh_config Lima writes for an instance.
func (p Paths) LimaSSHConfig(instance string) string {
	return filepath.Join(p.LimaHome, instance, "ssh.config")
}
