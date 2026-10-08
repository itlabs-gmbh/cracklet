package guest

import (
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
)

func TestRelayInstalledWhileCapsAreGranted(t *testing.T) {
	plan, err := Render([]cap.Cap{claudeLike()}, data)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Relay {
		t.Fatalf("a granted cap must bring the broker socket relay")
	}
	manifest := strings.Join(plan.Manifest(), "\n")
	for _, unit := range []string{RelaySocketUnit, RelayServiceUnit} {
		if !strings.Contains(manifest, "file:"+unit) {
			t.Errorf("manifest lacks %s:\n%s", unit, manifest)
		}
	}
	script, err := plan.ApplyScript(State{})
	if err != nil {
		t.Fatal(err)
	}
	socket := decodeHeredoc(t, script, RelaySocketUnit)
	if !strings.Contains(socket, "ListenStream=/run/cracklet/broker.sock") || !strings.Contains(socket, "SocketMode=0600") ||
		!strings.Contains(socket, "RemoveOnStop=yes") {
		t.Errorf("socket unit:\n%s", socket)
	}
	service := decodeHeredoc(t, script, RelayServiceUnit)
	if !strings.Contains(service, "systemd-socket-proxyd 127.0.0.1:7777") {
		t.Errorf("service unit:\n%s", service)
	}
	if !strings.Contains(script, "systemctl --quiet enable --now cracklet-broker.socket") {
		t.Errorf("relay is not started:\n%s", script)
	}
	if strings.Contains(script, "systemctl disable") {
		t.Errorf("relay must not be stopped while caps are granted:\n%s", script)
	}
}

func TestRelayRemovedWithLastGrant(t *testing.T) {
	plan, err := Render(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Relay {
		t.Fatalf("no caps, no relay")
	}
	st := State{Manifest: []string{"file:" + RelaySocketUnit, "file:" + RelayServiceUnit}}
	script, err := plan.ApplyScript(st)
	if err != nil {
		t.Fatal(err)
	}
	stop := strings.Index(script, "systemctl disable --now cracklet-broker.socket cracklet-broker.service")
	rm := strings.Index(script, "rm -f '"+RelaySocketUnit+"'")
	if stop < 0 || rm < 0 || stop > rm {
		t.Fatalf("relay must be stopped before its units are removed:\n%s", script)
	}
	if strings.Contains(script, "systemctl enable") {
		t.Errorf("relay must not be started without grants:\n%s", script)
	}
}

func TestRelayUnitsAreReserved(t *testing.T) {
	for name, g := range map[string]cap.Guest{
		"file":  {Files: []cap.File{{Path: RelayServiceUnit, Content: "x"}}},
		"block": {Blocks: []cap.Block{{Path: RelaySocketUnit, Content: "x"}}},
		"json":  {JSONMerge: []cap.JSONMerge{{Path: RelayServiceUnit, Key: "k", Value: 1}}},
	} {
		squatter := cap.Cap{Name: "squat", Guest: g}
		if _, err := Render([]cap.Cap{squatter}, data); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: a cap must not overwrite the relay units, got %v", name, err)
		}
	}
}

// A path the shell resolves to the same file must not slip past the
// reservation or the conflict checks between caps.
func TestNonCanonicalPathsAreRejected(t *testing.T) {
	for _, p := range []string{
		"/etc/systemd/system/./cracklet-broker.service",
		"/etc//gitconfig",
		"/etc/gitconfig/",
		"/etc/./gitconfig",
	} {
		c := cap.Cap{Name: "x", Guest: cap.Guest{Files: []cap.File{{Path: p, Content: "x"}}}}
		if _, err := Render([]cap.Cap{c}, data); err == nil {
			t.Errorf("%s must be rejected", p)
		}
	}
}

func TestSystemctlOnlyAsRootOnSystemd(t *testing.T) {
	plan, err := Render([]cap.Cap{claudeLike()}, data)
	if err != nil {
		t.Fatal(err)
	}
	script, err := plan.ApplyScript(State{})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "systemctl ") && !strings.Contains(line, `[ "$(id -u)" = 0 ] && [ -d /run/systemd/system ]`) {
			t.Errorf("unguarded systemctl call: %s", line)
		}
	}
}

// Manifests written before paths had to be canonical may still list such
// paths; a revoke must clean them up rather than silently drop them.
func TestRevokeCleansLegacyNonCanonicalPaths(t *testing.T) {
	st := State{Manifest: []string{"file:/etc/./old.conf", "block:/etc/./gitconfig#forge"}}
	script, err := Plan{}.ApplyScript(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "rm -f '/etc/./old.conf'") {
		t.Errorf("legacy file not removed:\n%s", script)
	}
	if !strings.Contains(script, "sed '/^.* >>> cracklet:forge .*$/,/^.* <<< cracklet:forge$/d' '/etc/./gitconfig'") {
		t.Errorf("legacy block not removed:\n%s", script)
	}
	// The relaxed check for old entries still refuses traversal.
	st = State{Manifest: []string{"file:/etc/../root/x", "block:/etc/x/..#forge"}}
	if script, _ = (Plan{}).ApplyScript(st); strings.Contains(script, "/etc/../root/x") || strings.Contains(script, "/etc/x/..") {
		t.Errorf("traversal in a manifest must still be ignored:\n%s", script)
	}
}
