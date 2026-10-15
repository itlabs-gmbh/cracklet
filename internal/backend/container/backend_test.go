package container

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/host"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

func TestDoctorReportsVersionAndService(t *testing.T) {
	b, _, out := newTestBackend(t, newFake())
	if err := b.Doctor(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"container=true", "container 1.5.0", "service: running"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestDoctorRejectsOldMacOS(t *testing.T) {
	facts := func(context.Context) host.Facts {
		f := goodFacts(context.Background())
		f.MacOSVersion = "15.6"
		return f
	}
	b, fake, out := newTestBackend(t, newFake(), WithFacts(facts))
	if err := b.Doctor(context.Background()); err == nil || !strings.Contains(out.String(), "macOS 26") {
		t.Fatalf("macOS 15 must be fatal, got %v:\n%s", err, out.String())
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("no container call on a host that cannot run it:\n%s", fake.Dump())
	}
}

func TestPrepareInstallsCLIStartsServiceAndBuildsImage(t *testing.T) {
	f := newFake()
	f.down = true
	facts := func(context.Context) host.Facts {
		h := goodFacts(context.Background())
		h.HasContainer = false
		return h
	}
	b, fake, out := newTestBackend(t, f, WithFacts(facts))
	if err := b.Prepare(context.Background(), backend.PrepareOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"brew install container", "container system start --enable-kernel-install", "container build --tag " + ImageRef} {
		if !fake.CalledWithSuffix(want) && !called(fake.Calls(), want) {
			t.Errorf("missing %q in:\n%s", want, fake.Dump())
		}
	}
	if fake.CalledWithSuffix("ssh-keygen") {
		t.Error("an existing key must be kept")
	}
	if !strings.Contains(out.String(), "Building guest image") {
		t.Errorf("progress missing:\n%s", out.String())
	}
}

func TestPrepareSkipsBuildWhenImageIsCurrent(t *testing.T) {
	f := newFake()
	b, fake, out := newTestBackend(t, f)
	f.labels = imageLabels([]byte("ssh-ed25519 AAAA test"))
	if err := b.Prepare(context.Background(), backend.PrepareOptions{Profiles: []string{"paseo"}}); err != nil {
		t.Fatal(err)
	}
	if called(fake.Calls(), "container build") {
		t.Errorf("a current image must not be rebuilt:\n%s", fake.Dump())
	}
	if !strings.Contains(out.String(), "is current") || !strings.Contains(out.String(), "ignore --profile") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestPrepareRebuildsWhenKeyChanged(t *testing.T) {
	f := newFake()
	f.labels = map[string]string{labelRev: imageRev, labelKey: "someone-elses-key"}
	b, fake, _ := newTestBackend(t, f)
	if err := b.Prepare(context.Background(), backend.PrepareOptions{}); err != nil {
		t.Fatal(err)
	}
	if !called(fake.Calls(), "container build") {
		t.Errorf("a different key needs a new image:\n%s", fake.Dump())
	}
}

func TestPrepareRefusesSizingAndOldVersion(t *testing.T) {
	b, fake, _ := newTestBackend(t, newFake())
	err := b.Prepare(context.Background(), backend.PrepareOptions{CPUs: 8, Explicit: backend.SizeFlags{CPUs: true}})
	if err == nil || !strings.Contains(err.Error(), "no") || len(fake.Calls()) != 0 {
		t.Fatalf("explicit sizing must be refused before anything runs, got %v", err)
	}
	f := newFake()
	f.version = "1.4.1"
	b, _, _ = newTestBackend(t, f)
	if err := b.Prepare(context.Background(), backend.PrepareOptions{}); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("want version gate, got %v", err)
	}
}

func TestOlderThan(t *testing.T) {
	for _, tc := range []struct {
		have, want string
		older      bool
	}{{"1.5.0", "1.5.0", false}, {"1.4.9", "1.5.0", true}, {"1.10.0", "1.5.0", false}, {"2", "1.5.0", false}, {"0.9", "1.5.0", true}} {
		if got := olderThan(tc.have, tc.want); got != tc.older {
			t.Errorf("olderThan(%s, %s) = %v", tc.have, tc.want, got)
		}
	}
}

func TestCreateListStopStartRemove(t *testing.T) {
	f := newFake()
	b, fake, _ := newTestBackend(t, f)
	ctx := context.Background()
	created := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	info, err := b.Create(ctx, vm.Spec{Name: "dev", VCPUs: 2, MemMiB: 1024, Disk: "2G", Owner: "paseo", Slot: "1"}, created)
	if err != nil {
		t.Fatal(err)
	}
	if !fake.CalledWithSuffix("machine create " + ImageRef + " --name dev --cpus 2 --memory 1024M --home-mount none") {
		t.Errorf("create args:\n%s", fake.Dump())
	}
	if info.Name != "dev" || info.IP != "192.168.64.2" || info.State != vm.StateRunning || info.VCPUs != 2 || info.MemMiB != 1024 ||
		info.Owner != "paseo" || info.Slot != "1" || info.CreatedAt == nil || !info.CreatedAt.Equal(created) || info.Index != 2 {
		t.Errorf("info = %+v", info)
	}
	if fake.CalledWithSuffix("machine run --name dev --root -- true") {
		t.Error("create must not run a command in the fresh machine (first-run bug in container 1.5)")
	}
	auto, err := b.Create(ctx, vm.Spec{VCPUs: 1, MemMiB: 512, Disk: "2G"}, created)
	if err != nil || auto.Name != "vm1" {
		t.Fatalf("auto name = %q, %v; want vm1", auto.Name, err)
	}
	list, err := b.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "dev" || list[1].Name != "vm1" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if _, err := b.Create(ctx, vm.Spec{Name: "dev", VCPUs: 1, MemMiB: 512, Disk: "2G"}, created); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate name: %v", err)
	}

	stopped, err := b.Stop(ctx, "dev")
	if err != nil || stopped.State != vm.StateStopped {
		t.Fatalf("Stop = %+v, %v", stopped, err)
	}
	started, err := b.Start(ctx, "dev")
	if err != nil || started.State != vm.StateRunning || started.IP == "" {
		t.Fatalf("Start = %+v, %v", started, err)
	}
	if !fake.CalledWithSuffix("machine run --name dev --root -- true") {
		t.Errorf("start must boot through machine run:\n%s", fake.Dump())
	}

	// A guard with a different creation time means gc listed an older dev:
	// the current one survives and is reported gone.
	other := created.Add(-time.Hour)
	gone, err := b.Remove(ctx, "dev", &backend.RemoveGuard{Owner: "paseo", CreatedAt: &other})
	if err != nil || !gone {
		t.Fatalf("guarded remove of a recreated VM: gone=%v err=%v", gone, err)
	}
	if list, _ := b.List(ctx); len(list) != 2 {
		t.Errorf("dev must survive a stale guard, got %+v", list)
	}
	gone, err = b.Remove(ctx, "dev", &backend.RemoveGuard{Owner: "paseo", CreatedAt: &created})
	if err != nil || gone {
		t.Fatalf("guarded remove of the listed VM: gone=%v err=%v", gone, err)
	}
	if _, err := b.Remove(ctx, "dev", nil); err == nil {
		t.Error("removing a missing VM must fail")
	}
	if _, err := b.Remove(ctx, "vm1", nil); err != nil {
		t.Fatal(err)
	}
	if list, _ := b.List(ctx); len(list) != 0 {
		t.Errorf("want empty list, got %+v", list)
	}
	if !fake.CalledWithSuffix("machine rm vm1") {
		t.Errorf("rm not forwarded:\n%s", fake.Dump())
	}
}

func TestCreateCleansUpWhenSSHNeverAnswers(t *testing.T) {
	f := newFake()
	b, fake, _ := newTestBackend(t, f, WithSSHWait(30*time.Millisecond, 5*time.Millisecond), WithDialer(
		func(context.Context, string) (backend.Conn, error) { return nil, errors.New("refused") }))
	_, err := b.Create(context.Background(), vm.Spec{Name: "dead", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "did not answer on SSH") {
		t.Fatalf("want ssh timeout, got %v", err)
	}
	if !fake.CalledWithSuffix("machine rm dead") {
		t.Errorf("a machine that never came up must be removed:\n%s", fake.Dump())
	}
	if names, _ := b.recordNames(); len(names) != 0 {
		t.Errorf("record must be gone, got %v", names)
	}
}

func TestListDropsRecordsOfVanishedMachines(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	if _, err := b.Create(context.Background(), vm.Spec{Name: "gone", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	delete(f.machines, "gone") // removed behind cracklet's back
	f.mu.Unlock()
	list, err := b.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if names, _ := b.recordNames(); len(names) != 0 {
		t.Errorf("stale record must be dropped, got %v", names)
	}
}

func TestReadyStartsServiceAndRestoresRunningMachines(t *testing.T) {
	f := newFake()
	b, fake, out := newTestBackend(t, f)
	ctx := context.Background()
	for _, n := range []string{"up", "down"} {
		if _, err := b.Create(ctx, vm.Spec{Name: n, VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Stop(ctx, "down"); err != nil {
		t.Fatal(err)
	}
	// The Mac rebooted: the service is down and every machine stopped.
	f.mu.Lock()
	f.down = true
	for _, m := range f.machines {
		m.Status, m.IP = "stopped", ""
	}
	f.mu.Unlock()
	if err := b.Alive(ctx); err == nil {
		t.Fatal("Alive must fail while the service is down")
	}
	if err := b.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if !called(fake.Calls(), "container system start") {
		t.Errorf("service not started:\n%s", fake.Dump())
	}
	if !fake.CalledWithSuffix("machine run --name up --root -- true") || fake.CalledWithSuffix("machine run --name down --root -- true") {
		t.Errorf("only machines that were running come back:\n%s", fake.Dump())
	}
	if !strings.Contains(out.String(), "Restarted microVMs: up") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestDialAndProxyCommand(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	if got, want := b.SSHProxyCommand(), "/usr/local/bin/cracklet _dial %h %p"; got != want {
		t.Errorf("SSHProxyCommand = %q, want %q", got, want)
	}
	if _, err := b.Dial(context.Background(), "nope", 22); err == nil {
		t.Error("dialing an unknown VM must fail")
	}
	if _, err := b.Create(context.Background(), vm.Spec{Name: "dev", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	conn, err := b.Dial(context.Background(), "dev", 22)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestForwardsAreRefusedWithTheAddress(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	if _, err := b.Create(context.Background(), vm.Spec{Name: "web", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, err := b.Forward(context.Background(), "web", []vm.Forward{{Host: 8080, Guest: 80}})
	if err == nil || !strings.Contains(err.Error(), "192.168.64.2:PORT") {
		t.Errorf("want refusal with the direct address, got %v", err)
	}
	if b.Capabilities().Forwards {
		t.Error("Capabilities must say no forwards")
	}
}

func called(calls []string, needle string) bool {
	for _, c := range calls {
		if strings.Contains(c, needle) {
			return true
		}
	}
	return false
}
