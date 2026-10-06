package envdbin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

const testVersion = "v0.0.0-20261006143901-0b5ed29f8468"

func goFound(name string) (string, error) {
	if name == "go" {
		return "/usr/local/go/bin/go", nil
	}
	return "", errors.New("not found")
}

func goMissing(string) (string, error) { return "", errors.New("not found") }

// toolchain fakes `go mod download` and `go build`, writing size bytes to the -o path.
func toolchain(t *testing.T, moduleDir string, size int) runner.FakeHandler {
	t.Helper()
	return func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "go mod download -json "):
			return json.Marshal(map[string]string{"Dir": moduleDir})
		case strings.Contains(joined, " go build "):
			for i, a := range args {
				if a == "-o" && i+1 < len(args) {
					return nil, os.WriteFile(args[i+1], bytes.Repeat([]byte{0x7f}, size), 0o755)
				}
			}
			return nil, errors.New("go build without -o: " + joined)
		}
		return nil, errors.New("unexpected command: " + joined)
	}
}

func TestBuildCrossCompilesFromModuleCache(t *testing.T) {
	moduleDir := t.TempDir()
	fake := runner.NewFake(toolchain(t, moduleDir, 4096))
	b := Builder{Runner: fake, LookPath: goFound, Version: testVersion, TempDir: t.TempDir()}

	data, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(data) != 4096 {
		t.Errorf("want 4096 bytes, got %d", len(data))
	}
	if !fake.Called("go mod download -json " + Module + "@" + testVersion) {
		t.Errorf("module must be resolved at the running version:\n%s", fake.Dump())
	}
	var build []string
	for _, argv := range fake.Argv() {
		if argv[0] == "env" {
			build = argv
		}
	}
	if build == nil {
		t.Fatalf("no go build call:\n%s", fake.Dump())
	}
	for _, want := range []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64", "-C", moduleDir, "-trimpath", daemonPackage} {
		if !contains(build, want) {
			t.Errorf("go build must contain %q, got %q", want, build)
		}
	}
}

func TestBuildCleansUpTempDir(t *testing.T) {
	tmp := t.TempDir()
	b := Builder{Runner: runner.NewFake(toolchain(t, t.TempDir(), 4096)), LookPath: goFound, Version: testVersion, TempDir: tmp}
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Errorf("build directory must be removed, found %d entries", len(entries))
	}
}

func TestBuildRejectsTruncatedOutput(t *testing.T) {
	b := Builder{Runner: runner.NewFake(toolchain(t, t.TempDir(), 10)), LookPath: goFound, Version: testVersion, TempDir: t.TempDir()}
	if _, err := b.Build(context.Background()); err == nil || !strings.Contains(err.Error(), "too small") {
		t.Errorf("want 'too small' error, got %v", err)
	}
}

func TestBuildRequiresGoToolchain(t *testing.T) {
	b := Builder{Runner: runner.NewFake(toolchain(t, t.TempDir(), 4096)), LookPath: goMissing, Version: testVersion}
	_, err := b.Build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "install Go") {
		t.Errorf("want install-Go hint, got %v", err)
	}
}

func TestBuildRequiresPinnedModuleVersion(t *testing.T) {
	for _, v := range []string{"", "(devel)"} {
		b := Builder{Runner: runner.NewFake(toolchain(t, t.TempDir(), 4096)), LookPath: goFound, Version: v}
		_, err := b.Build(context.Background())
		if err == nil || !strings.Contains(err.Error(), "make build") {
			t.Errorf("version %q: want 'make build' hint, got %v", v, err)
		}
	}
}

func TestBuildSurfacesDownloadErrors(t *testing.T) {
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		return json.Marshal(map[string]string{"Error": "unknown revision"})
	})
	b := Builder{Runner: fake, LookPath: goFound, Version: testVersion}
	_, err := b.Build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unknown revision") {
		t.Errorf("want download error surfaced, got %v", err)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestBuildSurfacesCompileFailure(t *testing.T) {
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		if name == "go" {
			return json.Marshal(map[string]string{"Dir": t.TempDir()})
		}
		return nil, errors.New("exit status 1: undefined: foo")
	})
	b := Builder{Runner: fake, LookPath: goFound, Version: testVersion, TempDir: t.TempDir()}
	_, err := b.Build(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cross-compile cracklet-envd") || !strings.Contains(err.Error(), "undefined: foo") {
		t.Errorf("want wrapped compiler error, got %v", err)
	}
}

func TestModuleDirRejectsBadToolOutput(t *testing.T) {
	cases := map[string]string{
		"garbage":   "not json",
		"empty dir": `{"Dir": ""}`,
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			fake := runner.NewFake(func(string, []string) ([]byte, error) { return []byte(output), nil })
			b := Builder{Runner: fake, LookPath: goFound, Version: testVersion}
			if _, err := b.Build(context.Background()); err == nil || !strings.Contains(err.Error(), "resolve "+Module) && !strings.Contains(err.Error(), "parse") {
				t.Errorf("want resolve/parse error, got %v", err)
			}
		})
	}
}

func TestBuildFailsWhenTempDirUnwritable(t *testing.T) {
	b := Builder{Runner: runner.NewFake(toolchain(t, t.TempDir(), 4096)), LookPath: goFound, Version: testVersion,
		TempDir: "/nonexistent/cracklet"}
	if _, err := b.Build(context.Background()); err == nil || !strings.Contains(err.Error(), "build directory") {
		t.Errorf("want build directory error, got %v", err)
	}
}

func TestRunningVersionReportsBuildInfo(t *testing.T) {
	// Under `go test` the main module is always known; the exact value depends on
	// the toolchain, but it must never be mistaken for a pinned release.
	if v := RunningVersion(); isPinned(v) && !strings.Contains(v, "+dirty") && !strings.HasPrefix(v, "v0.0.0-") {
		t.Errorf("unexpected pinned version under go test: %q", v)
	}
}
