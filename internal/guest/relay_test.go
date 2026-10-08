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
	squatter := cap.Cap{Name: "squat", Guest: cap.Guest{Files: []cap.File{{Path: RelayServiceUnit, Content: "x"}}}}
	if _, err := Render([]cap.Cap{squatter}, data); err == nil || !strings.Contains(err.Error(), RelayServiceUnit) {
		t.Fatalf("a cap must not overwrite the relay units, got %v", err)
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
