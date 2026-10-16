package container

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/skiff/internal/vm"
)

func knownHosts(t *testing.T, b *Backend) string {
	t.Helper()
	data, err := os.ReadFile(b.paths.KnownHostsPath())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func TestCreatePinsTheHostKeyAndRemoveForgetsIt(t *testing.T) {
	f := newFake()
	b, fake, _ := newTestBackend(t, f)
	ctx := context.Background()
	if _, err := b.Create(ctx, vm.Spec{Name: "dev", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !fake.CalledWithSuffix("ssh-keyscan -q -t ed25519 -T 5 192.168.64.2") {
		t.Errorf("the key must be read from the fresh VM:\n%s", fake.Dump())
	}
	rec, _, err := b.loadRecord("dev")
	if err != nil || len(rec.HostKeys) != 1 || rec.HostKeys[0] != "ssh-ed25519 AAAA-dev-1" {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if got, want := knownHosts(t, b), "dev.skiff ssh-ed25519 AAAA-dev-1\n"; got != want {
		t.Errorf("known_hosts = %q, want %q", got, want)
	}
	if _, err := b.Create(ctx, vm.Spec{Name: "web", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := knownHosts(t, b); !strings.Contains(got, "dev.skiff ssh-ed25519 AAAA-dev-1\n") || !strings.Contains(got, "web.skiff ssh-ed25519 AAAA-web-2\n") {
		t.Errorf("known_hosts = %q", got)
	}
	// A VM removed and recreated under the same name is a different machine
	// with a different key: the old line must not linger, or ssh would refuse
	// the new VM as a changed host.
	if _, err := b.Remove(ctx, "dev", nil); err != nil {
		t.Fatal(err)
	}
	if got := knownHosts(t, b); strings.Contains(got, "dev.skiff") {
		t.Errorf("known_hosts still has dev after rm: %q", got)
	}
	if _, err := b.Create(ctx, vm.Spec{Name: "dev", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, want := knownHosts(t, b), "dev.skiff ssh-ed25519 AAAA-dev-3\nweb.skiff ssh-ed25519 AAAA-web-2\n"; got != want {
		t.Errorf("known_hosts = %q, want %q", got, want)
	}
}

func TestCreateWithoutAHostKeyIsUndone(t *testing.T) {
	f := newFake()
	f.noHostKey = true
	b, fake, _ := newTestBackend(t, f)
	_, err := b.Create(context.Background(), vm.Spec{Name: "dev", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("a VM whose key cannot be pinned is unusable with strict checking, got %v", err)
	}
	if !fake.CalledWithSuffix("machine rm dev") {
		t.Errorf("the VM must be removed again:\n%s", fake.Dump())
	}
	if names, _ := b.recordNames(); len(names) != 0 {
		t.Errorf("record must be gone, got %v", names)
	}
	f.noHostKey = false
	f.failing["ssh-keyscan"] = errors.New("ssh-keyscan: not found")
	if _, err := b.Create(context.Background(), vm.Spec{Name: "dev", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a failing keyscan must surface, got %v", err)
	}
}

func TestParseHostKeysKeepsOnlyTheScannedAddress(t *testing.T) {
	out := "# comment\n192.168.64.2 ssh-ed25519 AAAAC3 \n192.168.64.9 ssh-ed25519 AAAAother\n192.168.64.2 ssh-rsa\n\n"
	got := parseHostKeys(out, "192.168.64.2")
	if len(got) != 1 || got[0] != "ssh-ed25519 AAAAC3" {
		t.Errorf("parseHostKeys = %q", got)
	}
}

// TestPinRefusesARecordReplacedDuringTheScan: an auto-named VM removed and
// recreated under the same name while the first one's key was being read
// must neither get the old key nor be removed as the old one's leftover.
func TestPinRefusesARecordReplacedDuringTheScan(t *testing.T) {
	f := newFake()
	b, fake, _ := newTestBackend(t, f)
	f.beforeKeyscan = func() {
		rec, _, _ := b.loadRecord("dev")
		rec.ID = "someone-else"
		_ = b.saveRecord("dev", rec)
	}
	_, err := b.Create(context.Background(), vm.Spec{Name: "dev", VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "replaced") {
		t.Fatalf("want a replaced error, got %v", err)
	}
	if fake.CalledWithSuffix("machine rm dev") {
		t.Errorf("the other VM must not be removed:\n%s", fake.Dump())
	}
	rec, _, _ := b.loadRecord("dev")
	if rec.ID != "someone-else" || len(rec.HostKeys) != 0 {
		t.Errorf("the other VM's record must be untouched: %+v", rec)
	}
}

func TestEveryRecordGetsAUniqueID(t *testing.T) {
	b, _, _ := newTestBackend(t, newFake())
	ctx := context.Background()
	for _, name := range []string{"a", "b"} {
		if _, err := b.Create(ctx, vm.Spec{Name: name, VCPUs: 1, MemMiB: 512, Disk: "2G"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	ra, _, _ := b.loadRecord("a")
	rb, _, _ := b.loadRecord("b")
	if ra.ID == "" || ra.ID == rb.ID {
		t.Errorf("ids = %q, %q", ra.ID, rb.ID)
	}
}
