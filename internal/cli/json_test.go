package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/skiff/internal/app"
	"github.com/itlabs-gmbh/skiff/internal/backend/backendtest"
	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/runner"
)

// runSplit runs the CLI with separate stdout and stderr buffers so tests can
// check that --json keeps stdout machine-readable.
func runSplit(t *testing.T, fb *backendtest.Fake, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root := newRoot(builder(runner.NewFake(fakeHandler(t)), fb, t.TempDir()))
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestLsJSON(t *testing.T) {
	stdout, _, err := runSplit(t, seededBackend(), "ls", "--json")
	if err != nil {
		t.Fatalf("ls --json: %v", err)
	}
	var vms []app.VMInfo
	if err := json.Unmarshal([]byte(stdout), &vms); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout)
	}
	if len(vms) != 1 || vms[0].Name != "vm1" || vms[0].IP != "192.168.64.2" || vms[0].MemMiB != 1024 {
		t.Errorf("unexpected VMs: %+v", vms)
	}
}

func TestLsJSONEmptyIsArray(t *testing.T) {
	stdout, _, err := runSplit(t, backendtest.New(), "ls", "--json")
	if err != nil {
		t.Fatalf("ls --json: %v", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("no VMs must print [], got %q", stdout)
	}
}

func TestNewJSONKeepsProgressOffStdout(t *testing.T) {
	stdout, stderr, err := runSplit(t, seededBackend(), "new", "box", "--json")
	if err != nil {
		t.Fatalf("new --json: %v", err)
	}
	var info app.VMInfo
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, stdout)
	}
	if info.Name != "box" || info.IP != "192.168.64.3" {
		t.Errorf("unexpected VM: %+v", info)
	}
	if !strings.Contains(stderr, "box is running") {
		t.Errorf("progress should go to stderr, got %q", stderr)
	}
}

func TestNewWithoutJSONPrintsProgressToStdout(t *testing.T) {
	stdout, _, err := runSplit(t, seededBackend(), "new", "box")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !strings.Contains(stdout, "box is running") {
		t.Errorf("progress should stay on stdout without --json, got %q", stdout)
	}
}

func TestInspectPrintsDetails(t *testing.T) {
	stdout, _, err := runSplit(t, seededBackend(), "inspect", "vm1")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, want := range []string{"vm1", "running", "192.168.64.2", "1024M", "ssh vm1." + config.Instance} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect output lacks %q:\n%s", want, stdout)
		}
	}
}

func TestInspectJSON(t *testing.T) {
	stdout, _, err := runSplit(t, seededBackend(), "inspect", "vm1", "--json")
	if err != nil {
		t.Fatalf("inspect --json: %v", err)
	}
	var info app.VMInfo
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, stdout)
	}
	if info.Name != "vm1" || info.VCPUs != 2 {
		t.Errorf("unexpected VM: %+v", info)
	}
}

func TestInspectUnknownVM(t *testing.T) {
	stdout, _, err := runSplit(t, seededBackend(), "inspect", "ghost", "--json")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("want a 'does not exist' error, got %v", err)
	}
	if stdout != "" {
		t.Errorf("a failed inspect must not print to stdout, got %q", stdout)
	}
}

func TestInspectRejectsInvalidName(t *testing.T) {
	if _, _, err := runSplit(t, seededBackend(), "inspect", "../etc"); err == nil {
		t.Error("invalid VM name must be rejected")
	}
}
