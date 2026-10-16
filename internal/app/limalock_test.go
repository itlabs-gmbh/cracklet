package app

import (
	"context"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/skiff/internal/config"
	"github.com/itlabs-gmbh/skiff/internal/vm"
)

func TestStartAndNewRefuseDuringResize(t *testing.T) {
	a, fake, _ := newTestApp(t, defaultHandler(nil))
	release, err := limaOf(a).HoldForResize()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := a.StartVM(context.Background(), "vm1"); err == nil || !strings.Contains(err.Error(), "being resized") {
		t.Errorf("StartVM during a resize: got %v", err)
	}
	if _, err := a.NewVM(context.Background(), vm.Spec{Name: "vm2", VCPUs: 2, MemMiB: 1024, Disk: "2G"}); err == nil || !strings.Contains(err.Error(), "being resized") {
		t.Errorf("NewVM during a resize: got %v", err)
	}
	if fake.CalledWithSuffix(config.AgentPath+" start vm1") || len(fake.Calls()) != 0 {
		t.Errorf("nothing may reach the Lima VM during a resize:\n%s", fake.Dump())
	}
}

func TestStopIsAllowedDuringResize(t *testing.T) {
	a, fake, _ := newTestApp(t, defaultHandler(map[string]string{
		"stop": `{"name":"vm1","index":1,"ip":"172.16.1.2","state":"stopped"}`,
	}))
	release, err := limaOf(a).HoldForResize()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := a.StopVM(context.Background(), "vm1"); err != nil {
		t.Fatalf("stopping a microVM must stay possible: %v", err)
	}
	if !fake.CalledWithSuffix(config.AgentPath + " stop vm1") {
		t.Errorf("stop not forwarded:\n%s", fake.Dump())
	}
}
