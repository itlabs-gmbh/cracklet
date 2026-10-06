package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/agent"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

const runningList = `{"name":"cracklet","status":"Running","dir":"/tmp/lima/cracklet"}` + "\n"

// newTestApp wires an App against a fake runner whose behaviour is defined by handle.
func newTestApp(t *testing.T, handle runner.FakeHandler) (*App, *runner.Fake, *bytes.Buffer) {
	t.Helper()
	fake := runner.NewFake(handle)
	out := &bytes.Buffer{}
	paths := config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}
	return New(fake, paths, out, WithEnvd(fakeEnvd)), fake, out
}

// fakeEnvd stands in for the cross-compiled guest daemon so tests do not depend
// on `make envd` having run.
func fakeEnvd(context.Context) ([]byte, error) {
	return bytes.Repeat([]byte{0x7f}, 2048), nil
}

// defaultHandler simulates a prepared, running Lima instance with a current agent.
func defaultHandler(agentResponses map[string]string) runner.FakeHandler {
	return func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "limactl list"):
			return []byte(runningList), nil
		case strings.Contains(joined, "sha256sum"):
			return []byte(agent.Checksum() + "\n"), nil
		case strings.Contains(joined, config.AgentPath+" "):
			sub := args[len(args)-1]
			for i, a := range args {
				if a == config.AgentPath && i+1 < len(args) {
					sub = args[i+1]
				}
			}
			if resp, ok := agentResponses[sub]; ok {
				return []byte(resp), nil
			}
			return nil, errors.New("unexpected agent call: " + joined)
		}
		return nil, errors.New("unexpected command: " + joined)
	}
}

func TestNewRunsAgentWithSpec(t *testing.T) {
	app, fake, out := newTestApp(t, defaultHandler(map[string]string{
		"new": `{"name":"vm1","index":1,"ip":"172.16.1.2","state":"running"}`,
	}))
	info, err := app.NewVM(context.Background(), vm.Spec{Name: "vm1", VCPUs: 2, MemMiB: 1024, Disk: "2G"})
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	if info.IP != "172.16.1.2" || info.Name != "vm1" {
		t.Errorf("unexpected info: %+v", info)
	}
	want := "limactl shell cracklet -- sudo " + config.AgentPath + " new vm1 2 1024 2G snapshot"
	if !fake.Called(want) {
		t.Errorf("expected call %q, got:\n%s", want, fake.Dump())
	}
	if !strings.Contains(out.String(), "cracklet ssh vm1") {
		t.Errorf("output should hint at ssh usage, got:\n%s", out.String())
	}
}

func TestNewRejectsInvalidSpec(t *testing.T) {
	app, fake, _ := newTestApp(t, defaultHandler(nil))
	if _, err := app.NewVM(context.Background(), vm.Spec{Name: "Bad Name", VCPUs: 2, MemMiB: 1024, Disk: "2G"}); err == nil {
		t.Fatal("expected validation error")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("no commands should run for an invalid spec, got:\n%s", fake.Dump())
	}
}

func TestNewRequiresRunningInstance(t *testing.T) {
	app, _, _ := newTestApp(t, func(name string, args []string) ([]byte, error) {
		return []byte(`{"name":"cracklet","status":"Stopped"}` + "\n"), nil
	})
	_, err := app.NewVM(context.Background(), vm.Spec{Name: "vm1", VCPUs: 2, MemMiB: 1024, Disk: "2G"})
	if err == nil || !strings.Contains(err.Error(), "cracklet prepare") {
		t.Fatalf("expected error pointing at 'cracklet prepare', got %v", err)
	}
}

func TestAgentIsReinstalledWhenChecksumDiffers(t *testing.T) {
	installed := false
	app, fake, _ := newTestApp(t, func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "limactl list"):
			return []byte(runningList), nil
		case strings.Contains(joined, "sha256sum"):
			if !strings.Contains(joined, "-- sudo sh -c sha256sum") {
				t.Errorf("the checksum probe must run with sudo (root-owned directory): %s", joined)
			}
			return []byte("deadbeef\n"), nil
		case strings.HasSuffix(joined, "sh "+config.AgentPath+" 0755"):
			installed = true
			return nil, nil
		case strings.Contains(joined, config.AgentPath+" ls"):
			return []byte("[]"), nil
		}
		return nil, errors.New("unexpected command: " + joined)
	})
	if _, err := app.ListVMs(context.Background()); err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if !installed {
		t.Errorf("agent should have been reinstalled, calls:\n%s", fake.Dump())
	}
	if in, ok := fake.Input("sh " + config.AgentPath + " 0755"); !ok || in != agent.Script {
		t.Errorf("the embedded agent script must be streamed verbatim (got %d bytes, ok=%v)", len(in), ok)
	}
}

func TestListParsesAgentOutput(t *testing.T) {
	app, _, _ := newTestApp(t, defaultHandler(map[string]string{
		"ls": `[{"name":"vm1","index":1,"ip":"172.16.1.2","state":"running","vcpus":2,"mem_mib":1024}]`,
	}))
	vms, err := app.ListVMs(context.Background())
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 1 || vms[0].Name != "vm1" || vms[0].State != "running" || vms[0].VCPUs != 2 {
		t.Errorf("unexpected vms: %+v", vms)
	}
}

func TestRemoveCallsAgent(t *testing.T) {
	app, fake, _ := newTestApp(t, defaultHandler(map[string]string{"rm": ""}))
	if err := app.RemoveVM(context.Background(), "vm1"); err != nil {
		t.Fatalf("RemoveVM: %v", err)
	}
	if !fake.Called("limactl shell cracklet -- sudo " + config.AgentPath + " rm vm1") {
		t.Errorf("expected rm call, got:\n%s", fake.Dump())
	}
}

func TestRemoveRejectsInvalidName(t *testing.T) {
	app, fake, _ := newTestApp(t, defaultHandler(nil))
	if err := app.RemoveVM(context.Background(), "../etc"); err == nil {
		t.Fatal("expected validation error")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("no commands should run, got:\n%s", fake.Dump())
	}
}

func TestSSHArgsSeparateRemoteCommand(t *testing.T) {
	app, _, _ := newTestApp(t, defaultHandler(nil))
	args := app.sshArgs("vm1", []string{"uname", "-a"})
	joined := strings.Join(args, " ")
	if !strings.HasPrefix(joined, "-F ") || !strings.HasSuffix(joined, " vm1.cracklet -- uname -a") {
		t.Errorf("unexpected ssh args: %q", joined)
	}
	if got := app.sshArgs("vm1", nil); got[len(got)-1] != "vm1.cracklet" {
		t.Errorf("interactive sessions must not end with a separator: %q", got)
	}
}

func TestSSHArgsNeutraliseOptionLookalikes(t *testing.T) {
	app, _, _ := newTestApp(t, defaultHandler(nil))
	payload := "-oProxyCommand=touch /tmp/pwned"
	args := app.sshArgs("vm1", []string{payload})
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
		}
	}
	if sep < 0 || args[len(args)-1] != payload || sep > len(args)-2 {
		t.Fatalf("payload must follow the -- separator, got %q", args)
	}
	for _, a := range args[:sep] {
		if a == payload {
			t.Fatalf("payload leaked before the separator: %q", args)
		}
	}
}

func TestNewForwardsCanonicalDiskSize(t *testing.T) {
	app, fake, _ := newTestApp(t, defaultHandler(map[string]string{
		"new": `{"name":"vm1","index":1,"ip":"172.16.1.2","state":"running"}`,
	}))
	if _, err := app.NewVM(context.Background(), vm.Spec{Name: "vm1", VCPUs: 1, MemMiB: 256, Disk: " 4g "}); err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " new vm1 1 256 4G snapshot") {
		t.Errorf("disk size must be canonicalised, got:\n%s", fake.Dump())
	}
}

func TestListToleratesBrokenEntries(t *testing.T) {
	app, _, _ := newTestApp(t, defaultHandler(map[string]string{
		"ls": `[{"name":"ok","index":1,"ip":"172.16.1.2","state":"running","vcpus":2,"mem_mib":1024},` +
			`{"name":"bad","index":null,"ip":null,"state":"broken","vcpus":null,"mem_mib":null}]`,
	}))
	vms, err := app.ListVMs(context.Background())
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 2 || vms[1].State != "broken" || vms[1].IP != "" {
		t.Errorf("unexpected vms: %+v", vms)
	}
}
