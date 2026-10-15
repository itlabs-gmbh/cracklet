package container

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

func createDev(t *testing.T, b *Backend) {
	t.Helper()
	if _, err := b.Create(context.Background(), vm.Spec{Name: "dev", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestForwardIsRecordedAndStartsTheRelayAgent(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	ctx := context.Background()
	createDev(t, b)
	info, err := b.Forward(ctx, "dev", []vm.Forward{{Host: 8080, Guest: 80}})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Forwards) != 1 || info.Forwards[0] != (vm.Forward{Host: 8080, Guest: 80, State: "inactive"}) {
		t.Errorf("forwards = %+v; nothing listens yet", info.Forwards)
	}
	rec, _, _ := b.loadRecord("dev")
	if len(rec.Forwards) != 1 || rec.Forwards[0] != (vm.Forward{Host: 8080, Guest: 80}) {
		t.Errorf("record = %+v; the state must not be stored", rec)
	}
	// First forward: no relay runs, so one is spawned for this binary.
	if got := spawnsOf(b).exes; len(got) != 1 || got[0] != "/usr/local/bin/cracklet" {
		t.Errorf("relay spawns = %v", got)
	}
	// With the relay listening, the forward is active; while it holds the
	// relay lock no second one is spawned.
	f.listening[8080] = true
	hold := holdRelayLock(t, b)
	info, err = b.Forward(ctx, "dev", []vm.Forward{{Host: 5432, Guest: 5432}})
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Forwards) != 2 || info.Forwards[0].State != "active" || info.Forwards[1].State != "inactive" {
		t.Errorf("forwards = %+v", info.Forwards)
	}
	if n := spawnsOf(b).spawned(); n != 1 {
		t.Errorf("a running relay must not be spawned again, got %d spawns", n)
	}
	hold()
	// Same VM, same host port: the guest port is replaced, as on Lima.
	info, err = b.Forward(ctx, "dev", []vm.Forward{{Host: 8080, Guest: 8080}})
	if err != nil || len(info.Forwards) != 2 || info.Forwards[0].Guest != 8080 {
		t.Errorf("forwards = %+v, %v", info.Forwards, err)
	}
	list, err := b.List(ctx)
	if err != nil || len(list) != 1 || len(list[0].Forwards) != 2 {
		t.Errorf("List = %+v, %v", list, err)
	}
	info, err = b.Unforward(ctx, "dev", []int{8080, 9999})
	if err != nil || len(info.Forwards) != 1 || info.Forwards[0].Host != 5432 {
		t.Errorf("Unforward = %+v, %v; an unknown port is a no-op", info.Forwards, err)
	}
}

func TestForwardRefusesAPortAnotherVMOwns(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	ctx := context.Background()
	createDev(t, b)
	if _, err := b.Create(ctx, vm.Spec{Name: "web", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Forward(ctx, "dev", []vm.Forward{{Host: 8080, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	_, err := b.Forward(ctx, "web", []vm.Forward{{Host: 8080, Guest: 80}})
	if err == nil || !strings.Contains(err.Error(), "already forwarded to VM 'dev'") {
		t.Fatalf("want the Lima wording, got %v", err)
	}
	if rec, _, _ := b.loadRecord("web"); len(rec.Forwards) != 0 {
		t.Errorf("nothing must be stored for web: %+v", rec)
	}
	if _, err := b.Forward(ctx, "ghost", []vm.Forward{{Host: 8081, Guest: 80}}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("unknown VM: %v", err)
	}
	if _, err := b.Unforward(ctx, "ghost", []int{8080}); err == nil {
		t.Errorf("unforward on an unknown VM must fail: %v", err)
	}
	spawnsOf(b).fail = errors.New("fork: boom")
	if _, err := b.Forward(ctx, "web", []vm.Forward{{Host: 8081, Guest: 80}}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a relay that cannot start must be reported, got %v", err)
	}
}

func TestStartAndEveryCommandBringTheRelayBack(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	ctx := context.Background()
	createDev(t, b)
	if _, err := b.Forward(ctx, "dev", []vm.Forward{{Host: 8080, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	spawns := spawnsOf(b)
	if spawns.spawned() != 1 {
		t.Fatalf("spawns = %d", spawns.spawned())
	}
	// The relay is gone (logout): stop leaves it alone, start brings it back.
	if _, err := b.Stop(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Start(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if spawns.spawned() != 2 {
		t.Errorf("start must bring the relay back, spawns = %d", spawns.spawned())
	}
	// Any command passes through Ready, which does the same.
	if err := b.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if spawns.spawned() != 3 {
		t.Errorf("Ready must bring the relay back, spawns = %d", spawns.spawned())
	}
	// Without forwards there is nothing to spawn for.
	if _, err := b.Unforward(ctx, "dev", []int{8080}); err != nil {
		t.Fatal(err)
	}
	if err := b.Ready(ctx); err != nil || spawns.spawned() != 3 {
		t.Errorf("no forwards, no relay: spawns = %d, %v", spawns.spawned(), err)
	}
}

func TestReadyRestoresADeletedKnownHosts(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	createDev(t, b)
	if err := os.Remove(b.paths.KnownHostsPath()); err != nil {
		t.Fatal(err)
	}
	if err := b.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := knownHosts(t, b); !strings.Contains(got, "dev.cracklet ssh-ed25519") {
		t.Errorf("known_hosts after Ready = %q", got)
	}
}

func TestForwardSurvivesInTheRecordButNotTheVM(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	ctx := context.Background()
	createDev(t, b)
	if _, err := b.Forward(ctx, "dev", []vm.Forward{{Host: 8080, Guest: 80}}); err != nil {
		t.Fatal(err)
	}
	targets, err := b.forwardTargets()
	if err != nil || len(targets) != 1 || targets[8080] != (forwardTarget{VM: "dev", Guest: 80}) {
		t.Fatalf("targets = %+v, %v", targets, err)
	}
	if _, err := b.Remove(ctx, "dev", nil); err != nil {
		t.Fatal(err)
	}
	if targets, _ := b.forwardTargets(); len(targets) != 0 {
		t.Errorf("rm must take the forwards with it, got %+v", targets)
	}
}

// holdRelayLock takes relay.lock as a running relay would.
func holdRelayLock(t *testing.T, b *Backend) func() {
	t.Helper()
	release, err := b.holdRelayLock()
	if err != nil {
		t.Fatal(err)
	}
	return release
}
