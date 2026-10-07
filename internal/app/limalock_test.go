package app

import (
	"context"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

func TestLimaLockSharedAndExclusive(t *testing.T) {
	a, _, _ := newTestApp(t, defaultHandler(nil))
	first, err := a.holdLimaForMicroVM()
	if err != nil {
		t.Fatalf("first shared hold: %v", err)
	}
	second, err := a.holdLimaForMicroVM()
	if err != nil {
		t.Fatalf("microVM starts must not block each other: %v", err)
	}
	if _, err := a.holdLimaForResize(); err == nil || !strings.Contains(err.Error(), "starting a microVM") {
		t.Fatalf("a resize must not start while a microVM starts, got %v", err)
	}
	first()
	second()

	release, err := a.holdLimaForResize()
	if err != nil {
		t.Fatalf("exclusive hold after release: %v", err)
	}
	if _, err := a.holdLimaForMicroVM(); err == nil || !strings.Contains(err.Error(), "being resized") {
		t.Fatalf("a microVM must not start during a resize, got %v", err)
	}
	release()
	if release, err := a.holdLimaForMicroVM(); err != nil {
		t.Fatalf("shared hold after the resize: %v", err)
	} else {
		release()
	}
}

func TestStartAndNewRefuseDuringResize(t *testing.T) {
	a, fake, _ := newTestApp(t, defaultHandler(nil))
	release, err := a.holdLimaForResize()
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
	release, err := a.holdLimaForResize()
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
