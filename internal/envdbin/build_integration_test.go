package envdbin

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// TestBuildWithRealToolchain exercises the `go install ...@latest` path end to
// end: resolve a published module version and cross-compile the daemon from the
// module cache. It needs network and a Go toolchain, so it only runs on demand:
//
//	CRACKLET_REAL_TOOLCHAIN=1 go test -run TestBuildWithRealToolchain ./internal/envdbin/
func TestBuildWithRealToolchain(t *testing.T) {
	if os.Getenv("CRACKLET_REAL_TOOLCHAIN") == "" {
		t.Skip("set CRACKLET_REAL_TOOLCHAIN=1 to run")
	}
	var stderr bytes.Buffer
	r := &runner.Exec{Stdin: bytes.NewReader(nil), Stdout: io.Discard, Stderr: &stderr}
	b := Builder{Runner: r, LookPath: exec.LookPath, Version: testVersion, TempDir: t.TempDir()}

	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, stderr.String())
	}
	if !bytes.HasPrefix(data, []byte("\x7fELF")) {
		t.Fatalf("want an ELF binary, got %d bytes starting %q", len(data), data[:min(8, len(data))])
	}
	t.Logf("built %d-byte linux/arm64 daemon from %s@%s", len(data), Module, testVersion)
}
