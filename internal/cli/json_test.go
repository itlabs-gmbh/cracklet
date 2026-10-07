package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/app"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// runSplit runs the CLI with separate stdout and stderr buffers so tests can
// check that --json keeps stdout machine-readable.
func runSplit(t *testing.T, handler runner.FakeHandler, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runSplitWith(t, runner.NewFake(handler), args...)
}

// runSplitWith is runSplit with a fake the test keeps to inspect the calls.
func runSplitWith(t *testing.T, fake *runner.Fake, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root := newRoot(func(w io.Writer) (*app.App, error) {
		return app.New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, w), nil
	})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestLsJSON(t *testing.T) {
	stdout, _, err := runSplit(t, fakeHandler(t), "ls", "--json")
	if err != nil {
		t.Fatalf("ls --json: %v", err)
	}
	var vms []app.VMInfo
	if err := json.Unmarshal([]byte(stdout), &vms); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, stdout)
	}
	if len(vms) != 1 || vms[0].Name != "vm1" || vms[0].IP != "172.16.1.2" || vms[0].MemMiB != 1024 {
		t.Errorf("unexpected VMs: %+v", vms)
	}
}

func TestLsJSONEmptyIsArray(t *testing.T) {
	handler := func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), config.AgentPath+" ls") {
			return []byte(`[]`), nil
		}
		return fakeHandler(t)(name, args)
	}
	stdout, _, err := runSplit(t, handler, "ls", "--json")
	if err != nil {
		t.Fatalf("ls --json: %v", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("no VMs must print [], got %q", stdout)
	}
}

func TestNewJSONKeepsProgressOffStdout(t *testing.T) {
	stdout, stderr, err := runSplit(t, fakeHandler(t), "new", "box", "--json")
	if err != nil {
		t.Fatalf("new --json: %v", err)
	}
	var info app.VMInfo
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, stdout)
	}
	if info.Name != "box" || info.IP != "172.16.2.2" {
		t.Errorf("unexpected VM: %+v", info)
	}
	if !strings.Contains(stderr, "box is running") {
		t.Errorf("progress should go to stderr, got %q", stderr)
	}
}

func TestNewWithoutJSONPrintsProgressToStdout(t *testing.T) {
	stdout, _, err := runSplit(t, fakeHandler(t), "new", "box")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !strings.Contains(stdout, "box is running") {
		t.Errorf("progress should stay on stdout without --json, got %q", stdout)
	}
}

func TestInspectPrintsDetails(t *testing.T) {
	stdout, _, err := runSplit(t, fakeHandler(t), "inspect", "vm1")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, want := range []string{"vm1", "running", "172.16.1.2", "1024M", "ssh vm1." + config.Instance} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect output lacks %q:\n%s", want, stdout)
		}
	}
}

func TestInspectJSON(t *testing.T) {
	stdout, _, err := runSplit(t, fakeHandler(t), "inspect", "vm1", "--json")
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
	stdout, _, err := runSplit(t, fakeHandler(t), "inspect", "ghost", "--json")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("want a 'does not exist' error, got %v", err)
	}
	if stdout != "" {
		t.Errorf("a failed inspect must not print to stdout, got %q", stdout)
	}
}

func TestInspectRejectsInvalidName(t *testing.T) {
	if _, _, err := runSplit(t, fakeHandler(t), "inspect", "../etc"); err == nil {
		t.Error("invalid VM name must be rejected")
	}
}
