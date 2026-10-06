package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

const runningWithForward = `{"name":"web","index":1,"ip":"172.16.1.2","state":"running","vcpus":2,"mem_mib":1024,` +
	`"forwards":[{"host":8080,"guest":80,"state":"active"}]}`

func forwardApp(t *testing.T, responses map[string]string, probeResult bool) (*App, *runner.Fake, *bytes.Buffer, *[]int) {
	t.Helper()
	fake := runner.NewFake(defaultHandler(responses))
	out := &bytes.Buffer{}
	probed := &[]int{}
	app := New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, out,
		WithPortProbe(func(_ context.Context, port int) bool {
			*probed = append(*probed, port)
			return probeResult
		}),
		WithPortCheck(func(int) bool { return false }))
	return app, fake, out, probed
}

func TestForwardRefusesBusyHostPorts(t *testing.T) {
	fake := runner.NewFake(defaultHandler(map[string]string{"forward": runningWithForward}))
	app := New(fake, config.Paths{Home: t.TempDir()}, &bytes.Buffer{},
		WithPortProbe(func(context.Context, int) bool { return true }),
		WithPortCheck(func(port int) bool { return port == 8080 }))
	_, err := app.Forward(context.Background(), "web", []string{"8080:80"})
	if err == nil || !strings.Contains(err.Error(), "already in use on this Mac") {
		t.Fatalf("expected busy-port error, got %v", err)
	}
	if fake.CalledWithSuffix(" forward web 8080:80") {
		t.Error("the agent must not be asked to forward a busy port")
	}
	_, err = app.NewVM(context.Background(), vm.Spec{Name: "web", VCPUs: 1, MemMiB: 256, Disk: "2G", Forwards: []string{"8080"}})
	if err == nil || fake.CalledWithSuffix(" new web 1 256 2G snapshot") {
		t.Errorf("new must refuse busy ports before creating the VM: err=%v\n%s", err, fake.Dump())
	}
}

func TestHostPortBusyAgainstRealListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen on loopback")
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if !hostPortBusy(port) {
		t.Errorf("port %d has a listener and must be reported busy", port)
	}
	ln.Close()
	if hostPortBusy(port) {
		t.Errorf("port %d is free again and must not be reported busy", port)
	}
}

func TestForwardAddsAndProbes(t *testing.T) {
	app, fake, out, probed := forwardApp(t, map[string]string{"forward": runningWithForward}, true)
	info, err := app.Forward(context.Background(), "web", []string{"8080:80"})
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " forward web 8080:80") {
		t.Errorf("agent call missing:\n%s", fake.Dump())
	}
	if len(info.Forwards) != 1 || info.Forwards[0].Host != 8080 {
		t.Errorf("unexpected info: %+v", info)
	}
	if len(*probed) != 1 || (*probed)[0] != 8080 || !strings.Contains(out.String(), "localhost:8080 -> web:80 ready") {
		t.Errorf("probe/report mismatch: probed=%v out=%q", *probed, out.String())
	}
}

func TestForwardCanonicalisesSpecs(t *testing.T) {
	app, fake, _, _ := forwardApp(t, map[string]string{"forward": runningWithForward}, true)
	if _, err := app.Forward(context.Background(), "web", []string{"3000", " 9000:90 "}); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " forward web 3000:3000 9000:90") {
		t.Errorf("specs must be canonical HOST:GUEST:\n%s", fake.Dump())
	}
}

func TestForwardReportsStoredForStoppedVM(t *testing.T) {
	stopped := strings.Replace(runningWithForward, `"state":"running"`, `"state":"stopped"`, 1)
	app, _, out, probed := forwardApp(t, map[string]string{"forward": stopped}, true)
	if _, err := app.Forward(context.Background(), "web", []string{"8080:80"}); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if len(*probed) != 0 || !strings.Contains(out.String(), "applied when web starts") {
		t.Errorf("stopped VMs must not be probed: probed=%v out=%q", *probed, out.String())
	}
}

func TestForwardWarnsWhenHostPortNotReachable(t *testing.T) {
	app, _, out, _ := forwardApp(t, map[string]string{"forward": runningWithForward}, false)
	if _, err := app.Forward(context.Background(), "web", []string{"8080:80"}); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if !strings.Contains(out.String(), "not reachable yet") {
		t.Errorf("expected a warning, got %q", out.String())
	}
}

func TestForwardRejectsBadSpecsBeforeRunning(t *testing.T) {
	app, fake, _, _ := forwardApp(t, nil, true)
	if _, err := app.Forward(context.Background(), "web", []string{"80"}); err == nil {
		t.Fatal("privileged host port must be rejected")
	}
	if _, err := app.Forward(context.Background(), "Bad", []string{"8080"}); err == nil {
		t.Fatal("invalid name must be rejected")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("nothing should run:\n%s", fake.Dump())
	}
}

func TestForwardWithoutSpecsDescribes(t *testing.T) {
	app, fake, _, _ := forwardApp(t, map[string]string{"ls": "[" + runningWithForward + "]"}, true)
	info, err := app.Forward(context.Background(), "web", nil)
	if err != nil || len(info.Forwards) != 1 {
		t.Fatalf("expected the existing forward, got %+v, %v", info, err)
	}
	if fake.CalledWithSuffix(" forward web") {
		t.Error("listing must not call the agent's forward command")
	}
	if _, err := app.Forward(context.Background(), "nope", nil); err == nil {
		t.Error("unknown VM must be reported")
	}
}

func TestUnforward(t *testing.T) {
	app, fake, out, _ := forwardApp(t, map[string]string{"unforward": strings.Replace(runningWithForward, `[{"host":8080,"guest":80,"state":"active"}]`, "[]", 1)}, true)
	info, err := app.Unforward(context.Background(), "web", []int{8080, 3000})
	if err != nil {
		t.Fatalf("Unforward: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath+" unforward web 8080 3000") || len(info.Forwards) != 0 {
		t.Errorf("unexpected call/info: %+v\n%s", info, fake.Dump())
	}
	if !strings.Contains(out.String(), "removed forward localhost:8080") {
		t.Errorf("unexpected output %q", out.String())
	}
	if _, err := app.Unforward(context.Background(), "web", nil); err == nil {
		t.Error("empty port list must be rejected")
	}
	if _, err := app.Unforward(context.Background(), "web", []int{0}); err == nil {
		t.Error("invalid port must be rejected")
	}
}

func TestNewAppliesForwards(t *testing.T) {
	app, fake, _, _ := forwardApp(t, map[string]string{
		"new":     `{"name":"web","index":1,"ip":"172.16.1.2","state":"running","forwards":[]}`,
		"forward": runningWithForward,
	}, true)
	info, err := app.NewVM(context.Background(), vm.Spec{Name: "web", VCPUs: 1, MemMiB: 256, Disk: "2G", Forwards: []string{"8080:80"}})
	if err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath+" forward web 8080:80") || len(info.Forwards) != 1 {
		t.Errorf("forwards not applied after creation: %+v\n%s", info, fake.Dump())
	}
}

func TestForwardSurfacesAgentErrors(t *testing.T) {
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), " forward ") {
			return nil, errors.New("host port 8080 is already forwarded to VM 'other'")
		}
		return defaultHandler(nil)(name, args)
	})
	app := New(fake, config.Paths{Home: t.TempDir()}, &bytes.Buffer{},
		WithPortProbe(func(context.Context, int) bool { return true }),
		WithPortCheck(func(int) bool { return false }))
	if _, err := app.Forward(context.Background(), "web", []string{"8080"}); err == nil || !strings.Contains(err.Error(), "already forwarded") {
		t.Fatalf("expected agent error, got %v", err)
	}
}
