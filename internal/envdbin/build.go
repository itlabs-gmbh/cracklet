package envdbin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// Module is cracklet's import path; the daemon is built from the same module
// version as the running CLI so host and guest never drift apart.
const Module = "github.com/itlabs-gmbh/cracklet"

// daemonPackage is the daemon's package, relative to the module root.
const daemonPackage = "./cmd/cracklet-envd"

// develVersion is what debug.ReadBuildInfo reports for a plain `go build` in a checkout.
const develVersion = "(devel)"

// Builder cross-compiles cracklet-envd for linux/arm64 with the host Go toolchain.
type Builder struct {
	Runner   runner.Runner
	LookPath func(name string) (string, error)
	// Version is the module version of the running cracklet (see RunningVersion).
	Version string
	// TempDir hosts the build output; empty means os.TempDir.
	TempDir string
}

// RunningVersion reports the module version this binary was built from, or ""
// when the build carries no module information.
func RunningVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Version
}

// Build resolves the module source in the Go module cache and cross-compiles
// the daemon from it. It fails with actionable hints when the toolchain is
// missing or the running cracklet is a development build without a version.
func (b Builder) Build(ctx context.Context) ([]byte, error) {
	if _, err := b.LookPath("go"); err != nil {
		return nil, errors.New("cracklet-envd is not embedded in this cracklet and 'go' was not found; " +
			"install Go (https://go.dev/dl) or build cracklet from a checkout with 'make build'")
	}
	if !isPinned(b.Version) {
		return nil, fmt.Errorf("cracklet-envd is not embedded and this cracklet has no module version (%q); "+
			"build it from a checkout with 'make build' so the daemon gets embedded", b.Version)
	}
	dir, err := b.moduleDir(ctx)
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(b.TempDir, "cracklet-envd-")
	if err != nil {
		return nil, fmt.Errorf("create build directory: %w", err)
	}
	defer os.RemoveAll(work)
	return b.compile(ctx, dir, filepath.Join(work, "cracklet-envd"))
}

// isPinned accepts real module versions (v1.2.3, pseudo-versions) and rejects
// the placeholders Go uses for local builds.
func isPinned(v string) bool {
	return strings.HasPrefix(v, "v") && v != develVersion
}

// moduleDir asks the go command for the module's directory in the module cache,
// downloading it first if `go install` has not already done so.
func (b Builder) moduleDir(ctx context.Context) (string, error) {
	ref := Module + "@" + b.Version
	out, err := b.Runner.Output(ctx, "go", "mod", "download", "-json", ref)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	var info struct {
		Dir   string `json:"Dir"`
		Error string `json:"Error"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("parse 'go mod download' output for %s: %w", ref, err)
	}
	if info.Error != "" {
		return "", fmt.Errorf("resolve %s: %s", ref, info.Error)
	}
	if info.Dir == "" {
		return "", fmt.Errorf("resolve %s: 'go mod download' reported no directory", ref)
	}
	return info.Dir, nil
}

// compile runs the same cross-compile as `make envd`, rooted in dir. The target
// platform is passed through env(1) because Runner has no environment hook.
func (b Builder) compile(ctx context.Context, dir, out string) ([]byte, error) {
	err := b.Runner.Run(ctx, "env", "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64",
		"go", "build", "-C", dir, "-trimpath", "-ldflags=-s -w", "-o", out, daemonPackage)
	if err != nil {
		return nil, fmt.Errorf("cross-compile cracklet-envd: %w", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("read built cracklet-envd: %w", err)
	}
	if len(data) < MinSize {
		return nil, fmt.Errorf("built cracklet-envd is too small (%d bytes)", len(data))
	}
	return data, nil
}
