package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lastLine returns the final line of the agent's combined output, where a
// command prints its JSON result after the diagnostics.
func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// TestAgentStartMarksAndStopClearsAutostart: the autostart marker records
// whether the user wants a VM running, so it survives a crash or a failed
// boot and only an explicit stop removes it.
func TestAgentStartMarksAndStopClearsAutostart(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "vms", "vm1", "autostart")
	out, err := runAgentFuncs(t, root, `
		mkdir -p "$VMS_DIR/vm1"; echo 1 > "$VMS_DIR/vm1/index"; echo '{}' > "$VMS_DIR/vm1/config.json"
		unit_active() { return 0; }; add_host_entry() { :; }
		start_vm vm1
		[[ -e $VMS_DIR/vm1/autostart ]] && echo marked
		stop_vm() { :; }; vm_json() { :; }
		cmd_stop vm1
		[[ -e $VMS_DIR/vm1/autostart ]] || echo cleared`)
	if err != nil || !strings.Contains(out, "marked") || !strings.Contains(out, "cleared") {
		t.Fatalf("start/stop marker: %q, %v", out, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("marker still present after stop: %v", err)
	}
}

// TestAgentRestoreBootsMarkedVMsOncePerBoot: restore starts only the VMs
// that were running, keeps going when one of them fails, and the second time
// within the same boot of the Lima VM only repeats the first result, so the
// CLI still reports what the boot unit restored.
func TestAgentRestoreBootsMarkedVMsOncePerBoot(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	t.Setenv("RESTORED_FLAG", filepath.Join(root, "run", "restored"))
	snippet := `
		for vm in alpha broken off zeta; do mkdir -p "$VMS_DIR/$vm"; done
		touch "$VMS_DIR/alpha/autostart" "$VMS_DIR/broken/autostart" "$VMS_DIR/zeta/autostart"
		# broken fails half-way: set -e must stop it, not the loop
		start_vm() { echo "booting $1"; [[ $1 != broken ]] || false; echo "booted $1"; }
		cmd_restore
		cmd_restore`
	out, err := runAgentFuncs(t, root, snippet)
	if err != nil {
		t.Fatalf("restore failed: %v\n%s", err, out)
	}
	for _, want := range []string{"booted alpha", "booted zeta", "booting broken"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"booted broken", "booting off"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, out)
		}
	}
	var results []struct{ Started, Failed []string }
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "{") {
			var r struct{ Started, Failed []string }
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				t.Fatalf("restore printed invalid JSON %q: %v", line, err)
			}
			results = append(results, r)
		}
	}
	if len(results) != 2 {
		t.Fatalf("want one JSON result per restore, got %d:\n%s", len(results), out)
	}
	first, second := results[0], results[1]
	if strings.Join(first.Started, ",") != "alpha,zeta" || strings.Join(first.Failed, ",") != "broken" {
		t.Errorf("first restore: %+v", first)
	}
	if strings.Join(second.Started, ",") != "alpha,zeta" || strings.Join(second.Failed, ",") != "broken" {
		t.Errorf("second restore must repeat the first result: %+v", second)
	}
	if strings.Count(out, "booted alpha") != 1 || strings.Count(out, "booting broken") != 1 {
		t.Errorf("the second restore must not boot again:\n%s", out)
	}
}

// TestAgentRestoreSkipsRunningVMs: a VM someone started before restore got
// the lock is left alone.
func TestAgentRestoreSkipsRunningVMs(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	t.Setenv("RESTORED_FLAG", filepath.Join(root, "run", "restored"))
	out, err := runAgentFuncs(t, root, `
		mkdir -p "$VMS_DIR/up"; touch "$VMS_DIR/up/autostart"
		unit_active() { return 0; }
		start_vm() { echo "booting $1"; }
		cmd_restore`)
	if err != nil || strings.Contains(out, "booting") || lastLine(out) != `{"started":[],"failed":[]}` {
		t.Errorf("restore of a running VM: %q, %v", out, err)
	}
}

// TestAgentRestoreUnitEnabled: prepare installs a boot unit that runs
// restore, so microVMs come back however the Lima VM was started.
func TestAgentRestoreUnitEnabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SYSTEMD_DIR", filepath.Join(root, "systemd"))
	out, err := runAgentFuncs(t, root, `
		systemctl() { echo "systemctl $*"; }
		install_restore_unit`)
	if err != nil {
		t.Fatalf("install_restore_unit failed: %v\n%s", err, out)
	}
	unit, err := os.ReadFile(filepath.Join(root, "systemd", "cracklet-restore.service"))
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	for _, want := range []string{"Type=oneshot", "agent.sh restore", "After=network-online.target", "WantedBy=multi-user.target"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if !strings.Contains(out, "systemctl daemon-reload") || !strings.Contains(out, "systemctl enable --quiet cracklet-restore.service") {
		t.Errorf("unit not enabled:\n%s", out)
	}
}
