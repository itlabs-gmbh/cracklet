package app

import (
	"context"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/grant"
)

// TestBackendSwitchRefusedWhileLimaHasVMs: gc would otherwise prune the
// grants of VMs the new backend cannot list.
func TestBackendSwitchRefusedWhileLimaHasVMs(t *testing.T) {
	a, _, _ := newTestApp(t, defaultHandler(map[string]string{
		"ls": `[{"name":"vm1","index":1,"ip":"172.16.1.2","state":"running"}]`,
	}))
	// The stored choice is lima (default); the App was built for container.
	a.backend = containerBackend(a)
	err := a.CheckBackendSwitch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "still has VMs (vm1)") {
		t.Fatalf("want refusal naming vm1, got %v", err)
	}
}

func TestBackendSwitchRefusedWhileGrantsExist(t *testing.T) {
	a, fake, _ := newTestApp(t, defaultHandler(map[string]string{"ls": `[]`}))
	if err := a.grantStore().Save("old", grant.Set{}.Add(grant.Grant{Cap: "claude"})); err != nil {
		t.Fatal(err)
	}
	a.backend = containerBackend(a)
	err := a.CheckBackendSwitch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "grants for old") {
		t.Fatalf("want refusal naming the grant, got %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("host state alone decides, no command needed:\n%s", fake.Dump())
	}
}

func TestBackendSwitchAllowedWhenNothingIsStranded(t *testing.T) {
	a, _, _ := newTestApp(t, defaultHandler(map[string]string{"ls": `[]`}))
	a.backend = containerBackend(a)
	if err := a.CheckBackendSwitch(context.Background()); err != nil {
		t.Fatalf("an empty lima backend must not block the switch: %v", err)
	}
	if err := a.RememberBackend(); err != nil {
		t.Fatal(err)
	}
	if name, _ := backend.LoadName(a.paths); name != backend.NameContainer {
		t.Errorf("stored %q", name)
	}
	// Same backend again: no check needed, nothing to refuse.
	if err := a.CheckBackendSwitch(context.Background()); err != nil {
		t.Errorf("staying on the same backend: %v", err)
	}
}
