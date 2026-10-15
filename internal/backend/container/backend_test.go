package container

import (
	"context"
	"errors"
	"os"
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
		if !called(fake.Calls(), want) {
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
		bad        bool
	}{{"1.5.0", "1.5.0", false, false}, {"1.4.9", "1.5.0", true, false}, {"1.10.0", "1.5.0", false, false},
		{"2", "1.5.0", false, false}, {"0.9", "1.5.0", true, false}, {"1.6.0-rc1", "1.5.0", false, false},
		{"garbage", "1.5.0", false, true}, {"", "1.5.0", false, true}} {
		got, err := olderThan(tc.have, tc.want)
		if (err != nil) != tc.bad || got != tc.older {
			t.Errorf("olderThan(%q, %s) = %v, %v", tc.have, tc.want, got, err)
		}
	}
}

// TestListDoesNotPruneARecordBeingCreated: a record is written before its
// machine shows up in `machine ls`; an unlocked List must leave it alone.
func TestListDoesNotPruneARecordBeingCreated(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	if err := b.saveRecord("fresh", record{Running: true}); err != nil {
		t.Fatal(err)
	}
	list, err := b.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("List = %+v, %v; the machine does not exist yet", list, err)
	}
	if names, _ := b.recordNames(); len(names) != 1 {
		t.Fatalf("record must survive a concurrent List, got %v", names)
	}
	// Create prunes leftovers itself, under the lock.
	if _, err := b.Create(context.Background(), vm.Spec{Name: "other", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if names, _ := b.recordNames(); len(names) != 1 || names[0] != "other" {
		t.Errorf("stale record must be pruned by Create, got %v", names)
	}
}

func TestCreateFailureRemovesMachineAndRecord(t *testing.T) {
	f := newFake()
	b, fake, _ := newTestBackend(t, f)
	f.failing["container machine create"] = errors.New("apiserver: boom")
	// The API may have created the machine before failing.
	f.mu.Lock()
	f.machines["half"] = &machine{ID: "half", Status: "stopped"}
	f.mu.Unlock()
	_, err := b.Create(context.Background(), vm.Spec{VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want the create error, got %v", err)
	}
	if names, _ := b.recordNames(); len(names) != 0 {
		t.Errorf("record must be gone, got %v", names)
	}
	// vm1 was the auto-picked name (half is unrecorded but taken): rm only if it exists.
	if fake.CalledWithSuffix("machine rm vm1") {
		t.Errorf("no rm for a machine the API never created:\n%s", fake.Dump())
	}
	auto, err := b.Create(context.Background(), vm.Spec{VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now())
	if err == nil {
		t.Fatalf("create still failing, got %+v", auto)
	}
}

func TestAutoNameSkipsUnrecordedMachines(t *testing.T) {
	f := newFake()
	f.machines["vm1"] = &machine{ID: "vm1", Status: "running", IP: "192.168.64.50"}
	b, _, _ := newTestBackend(t, f)
	info, err := b.Create(context.Background(), vm.Spec{VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now())
	if err != nil || info.Name != "vm2" {
		t.Fatalf("auto name = %q, %v; want vm2 (vm1 belongs to the user)", info.Name, err)
	}
	if _, err := b.Create(context.Background(), vm.Spec{Name: "vm1", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("an unrecorded machine still blocks its name: %v", err)
	}
}

func TestRemoveOnlyTouchesCrackletsMachines(t *testing.T) {
	f := newFake()
	f.machines["theirs"] = &machine{ID: "theirs", Status: "running", IP: "192.168.64.60"}
	b, fake, _ := newTestBackend(t, f)
	if _, err := b.Remove(context.Background(), "theirs", nil); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("a machine without a record must not be removable, got %v", err)
	}
	if fake.CalledWithSuffix("machine rm theirs") {
		t.Errorf("machine rm must not run:\n%s", fake.Dump())
	}
	// A stale record whose machine is gone: unguarded rm reports it removed
	// (gone=false, like Lima), guarded rm reports gone.
	if err := b.saveRecord("stale", record{}); err != nil {
		t.Fatal(err)
	}
	gone, err := b.Remove(context.Background(), "stale", nil)
	if err != nil || gone {
		t.Errorf("unguarded remove of a stale record: gone=%v err=%v", gone, err)
	}
	if err := b.saveRecord("stale2", record{Owner: "x"}); err != nil {
		t.Fatal(err)
	}
	gone, err = b.Remove(context.Background(), "stale2", &backend.RemoveGuard{Owner: "x"})
	if err != nil || !gone {
		t.Errorf("guarded remove of a stale record: gone=%v err=%v", gone, err)
	}
}

func TestCorruptRecordStillListsAndRemoves(t *testing.T) {
	f := newFake()
	b, _, out := newTestBackend(t, f)
	if _, err := b.Create(context.Background(), vm.Spec{Name: "odd", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.recordPath("odd"), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := b.List(context.Background())
	if err != nil || len(list) != 1 || list[0].Name != "odd" || !strings.Contains(out.String(), "corrupt record") {
		t.Fatalf("List = %+v, %v\n%s", list, err, out.String())
	}
	if _, err := b.Remove(context.Background(), "odd", nil); err != nil {
		t.Fatalf("rm must still work: %v", err)
	}
}

func TestDialRefusesStoppedMachine(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	if _, err := b.Create(context.Background(), vm.Spec{Name: "s", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Stop(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Dial(context.Background(), "s", 22); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Errorf("want a stopped hint, got %v", err)
	}
}

func TestProxyCommandEscapesPercent(t *testing.T) {
	b, _, _ := newTestBackend(t, newFake(), WithExecutable("/Users/a%b/cracklet"))
	if got := b.SSHProxyCommand(); got != "/Users/a%%b/cracklet _dial %h %p" {
		t.Errorf("SSHProxyCommand = %q", got)
	}
}

func TestReadyIsANoopWhileAlive(t *testing.T) {
	b, fake, _ := newTestBackend(t, newFake())
	if err := b.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called(fake.Calls(), "system start") {
		t.Errorf("no start while the service runs:\n%s", fake.Dump())
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

func TestListHidesVanishedMachinesUntilPruned(t *testing.T) {
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
	// List is read-only (it cannot tell a vanished VM from one being created);
	// the stale record goes with the next locked operation.
	if _, err := b.Remove(context.Background(), "gone", nil); err != nil {
		t.Fatalf("rm of a vanished VM clears its record: %v", err)
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

func TestCapabilitiesHaveForwardsAndTheGuardRail(t *testing.T) {
	b, _, _ := newTestBackend(t, newFake())
	caps := b.Capabilities()
	if !caps.Forwards || !caps.GuestFirewall || caps.Snapshots || caps.Profiles || caps.Resize {
		t.Errorf("Capabilities = %+v", caps)
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

// TestReadyWaitsForARestoreInProgress: a second command must not see a
// running service and race ahead into machines that are still booting.
func TestReadyWaitsForARestoreInProgress(t *testing.T) {
	f := newFake()
	b, _, _ := newTestBackend(t, f)
	ctx := context.Background()
	if _, err := b.Create(ctx, vm.Spec{Name: "up", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.down = true
	f.machines["up"].Status, f.machines["up"].IP = "stopped", ""
	restoring := make(chan struct{})
	proceed := make(chan struct{})
	f.beforeRun = func() { close(restoring); <-proceed }
	f.mu.Unlock()

	first := make(chan error, 1)
	go func() { first <- b.Ready(ctx) }()
	<-restoring // the first Ready started the service and is mid-restore
	second := make(chan error, 1)
	go func() { second <- b.Ready(ctx) }()
	select {
	case err := <-second:
		t.Fatalf("second Ready returned (%v) while the restore was still running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(proceed)
	for _, ch := range []chan error{first, second} {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	m, err := b.machine(ctx, "up")
	if err != nil || m.state() != vm.StateRunning {
		t.Errorf("up must be running after both Ready calls: %+v %v", m, err)
	}
}

func TestPrepareRestoresMachinesAfterReboot(t *testing.T) {
	f := newFake()
	b, fake, _ := newTestBackend(t, f)
	ctx := context.Background()
	if _, err := b.Create(ctx, vm.Spec{Name: "up", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.down = true
	f.machines["up"].Status, f.machines["up"].IP = "stopped", ""
	f.labels = imageLabels([]byte("ssh-ed25519 AAAA test"))
	f.mu.Unlock()
	if err := b.Prepare(ctx, backend.PrepareOptions{}); err != nil {
		t.Fatal(err)
	}
	if !fake.CalledWithSuffix("machine run --name up --root -- true") {
		t.Errorf("prepare as the first command after a reboot must restore the VMs:\n%s", fake.Dump())
	}
}

// TestStopDuringBootKeepsTheVM: a stop that lands while Create still waits
// for sshd must not turn the creation into a failure that removes the disk.
func TestStopDuringBootKeepsTheVM(t *testing.T) {
	f := newFake()
	b, fake, _ := newTestBackend(t, f, WithSSHWait(2*time.Second, 5*time.Millisecond),
		WithDialer(func(context.Context, string) (backend.Conn, error) { return nil, errors.New("not yet") }))
	ctx := context.Background()
	done := make(chan error, 1)
	go func() {
		_, err := b.Create(ctx, vm.Spec{Name: "slow", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now())
		done <- err
	}()
	// Wait until the machine exists and Create is polling, then stop it.
	for {
		f.mu.Lock()
		_, exists := f.machines["slow"]
		f.mu.Unlock()
		if exists {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	stopped := time.Now()
	if _, err := b.Stop(ctx, "slow"); err != nil {
		t.Fatalf("stop during boot: %v", err)
	}
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "is kept") {
		t.Fatalf("Create must report the stop and keep the VM, got %v", err)
	}
	if time.Since(stopped) > time.Second {
		t.Errorf("Create must notice the stop at once, not wait for the ssh deadline (%s)", time.Since(stopped))
	}
	if fake.CalledWithSuffix("machine rm slow") {
		t.Errorf("stop promises to keep the disk:\n%s", fake.Dump())
	}
	list, err := b.List(ctx)
	if err != nil || len(list) != 1 || list[0].State != vm.StateStopped {
		t.Errorf("List = %+v, %v; want the stopped VM", list, err)
	}
}
