package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestScriptEmbedded(t *testing.T) {
	if !strings.HasPrefix(Script, "#!/usr/bin/env bash") {
		t.Fatalf("agent script should start with a bash shebang, got %q", firstLine(Script))
	}
	for _, cmd := range []string{"prepare", "new", "start", "stop", "rm", "ls", "restore", "forward", "unforward"} {
		if !strings.Contains(Script, "cmd_"+cmd+"()") {
			t.Errorf("agent script missing command handler cmd_%s", cmd)
		}
	}
}

func TestChecksumStable(t *testing.T) {
	a, b := Checksum(), Checksum()
	if a != b || len(a) != 64 {
		t.Fatalf("Checksum unstable or wrong length: %q %q", a, b)
	}
}

func TestScriptSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(Script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// runAgentFuncs sources the agent with CRACKLET_ROOT pointed at a temp dir and runs
// snippet with the pure helper functions available. Works with bash 3.2.
func runAgentFuncs(t *testing.T, root, snippet string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	script := filepath.Join(t.TempDir(), "agent.sh")
	if err := os.WriteFile(script, []byte(Script), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-c", `set -euo pipefail; source "$1"; unit_active() { return 1; }; fwd_active() { [[ $2 == 8080 ]]; }; `+snippet, "bash", script)
	cmd.Env = append(os.Environ(), "CRACKLET_ROOT="+root)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestAgentValidateName(t *testing.T) {
	root := t.TempDir()
	if _, err := runAgentFuncs(t, root, `validate_name dev-box-2`); err != nil {
		t.Errorf("valid name rejected: %v", err)
	}
	for _, bad := range []string{"Bad", "-x", "a_b", "x.y", "1abc"} {
		if _, err := runAgentFuncs(t, root, `validate_name "`+bad+`"`); err == nil {
			t.Errorf("invalid name %q accepted", bad)
		}
	}
}

func TestAgentIndexAndNameAllocation(t *testing.T) {
	root := t.TempDir()
	for name, idx := range map[string]string{"vm1": "1", "api": "2"} {
		dir := filepath.Join(root, "vms", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index"), []byte(idx+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runAgentFuncs(t, root, `allocate_index; free_auto_name; idx_mac 10; echo; idx_ip 3; idx_tap 3`)
	if err != nil {
		t.Fatalf("agent helpers failed: %v\n%s", err, out)
	}
	if out != "3\nvm2\n06:00:AC:10:0A:02\n172.16.3.2\ncracklet3" {
		t.Errorf("unexpected helper output:\n%s", out)
	}
}

func TestAgentWriteConfig(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vms", "box"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runAgentFuncs(t, root, `write_config box 7 2 1024;
		jq -r '."boot-source".boot_args, ."network-interfaces"[0].guest_mac, ."network-interfaces"[0].host_dev_name,
		       ."machine-config".vcpu_count, .drives[0].path_on_host, .vsock.uds_path' "$CRACKLET_ROOT/vms/box/config.json"`)
	if err != nil {
		t.Fatalf("write_config failed: %v\n%s", err, out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 6 {
		t.Fatalf("unexpected output:\n%s", out)
	}
	for i, want := range []string{
		"quiet loglevel=3 systemd.show_status=0 ip=172.16.7.2::172.16.7.1:255.255.255.252:box:eth0:off systemd.hostname=box",
		"06:00:AC:10:07:02",
		"cracklet7",
		"2",
		"./rootfs.ext4", // relative on purpose: snapshots are restored in other directories
		"./v.sock",
	} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "vms", "box", "config.json.tmp")); err == nil {
		t.Error("temporary config file must be renamed away")
	}
}

func TestAgentVMJSONReportsBrokenEntries(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vms", "half"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runAgentFuncs(t, root, `vm_json half`)
	if err != nil {
		t.Fatalf("vm_json must not fail on a broken VM: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"state":"broken"`) || !strings.Contains(out, `"name":"half"`) {
		t.Errorf("unexpected JSON: %s", out)
	}
}

func TestAgentValidateLabel(t *testing.T) {
	root := t.TempDir()
	if _, err := runAgentFuncs(t, root, `validate_label paseo-cracklet owner; validate_label 3 slot; validate_label - slot`); err != nil {
		t.Errorf("valid labels rejected: %v", err)
	}
	for _, bad := range []string{"", "-x", "a b", "a;b", "a/b", "$(id)"} {
		if _, err := runAgentFuncs(t, root, `validate_label "`+bad+`" owner`); err == nil {
			t.Errorf("invalid label %q accepted", bad)
		}
	}
}

func TestAgentVMJSONReportsMetadata(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	for _, name := range []string{"owned", "manual", "legacy"} {
		if err := os.MkdirAll(filepath.Join(root, "vms", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runAgentFuncs(t, root, `
		for n in owned manual legacy; do atomic_write "$(vm_dir $n)/index" 3; write_config $n 3 2 1024; done
		write_meta owned paseo 3; write_meta manual - -
		vm_json owned; vm_json manual; vm_json legacy`)
	if err != nil {
		t.Fatalf("vm_json failed: %v\n%s", err, out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("unexpected output:\n%s", out)
	}
	created := regexp.MustCompile(`"created_at":"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ"`)
	if !strings.Contains(lines[0], `"owner":"paseo","slot":"3"`) || !created.MatchString(lines[0]) {
		t.Errorf("owned VM: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"owner":null,"slot":null`) || !created.MatchString(lines[1]) {
		t.Errorf("a VM made by hand still records its creation time: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"owner":null,"slot":null,"created_at":null`) {
		t.Errorf("VMs from before metadata report nulls: %s", lines[2])
	}
}

func TestAgentVMJSONToleratesDamagedMetadata(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "vms", "odd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"owner":7,"slot":["x"],"created_at":"yesterday"`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runAgentFuncs(t, root, `atomic_write "$(vm_dir odd)/index" 4; write_config odd 4 2 1024; vm_json odd`)
	if err != nil {
		t.Fatalf("vm_json must not fail on damaged metadata: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"owner":null,"slot":null,"created_at":null`) || !strings.Contains(out, `"state":"stopped"`) {
		t.Errorf("unexpected JSON: %s", out)
	}
}

func TestAgentBrokenVMKeepsItsOwner(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vms", "half"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runAgentFuncs(t, root, `write_meta half paseo 2; vm_json half`)
	if err != nil {
		t.Fatalf("vm_json failed: %v\n%s", err, out)
	}
	// gc finds leftovers of an interrupted creation through their owner
	if !strings.Contains(out, `"state":"broken"`) || !strings.Contains(out, `"owner":"paseo","slot":"2"`) {
		t.Errorf("unexpected JSON: %s", out)
	}
}

func TestAgentMetaRejectsImpossibleDates(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	for name, date := range map[string]string{"bad1": "9999-99-99T99:99:99Z", "bad2": "2026-02-30T00:00:00Z", "good": "2026-02-28T23:59:59Z"} {
		dir := filepath.Join(root, "vms", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"owner":"x","created_at":"`+date+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runAgentFuncs(t, root, `meta_json bad1; meta_json bad2; meta_json good`)
	if err != nil {
		t.Fatalf("meta_json failed: %v\n%s", err, out)
	}
	// one unparsable date would make the CLI reject the whole listing
	want := `{"owner":"x","slot":null,"created_at":null}` + "\n" + `{"owner":"x","slot":null,"created_at":null}` + "\n" +
		`{"owner":"x","slot":null,"created_at":"2026-02-28T23:59:59Z"}`
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestAgentWriteMetaUsesGivenCreationTime(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vms", "box"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runAgentFuncs(t, root, `write_meta box paseo 1 2026-10-07T12:00:00Z; meta_json box`)
	if err != nil || out != `{"owner":"paseo","slot":"1","created_at":"2026-10-07T12:00:00Z"}` {
		t.Errorf("got %q, %v", out, err)
	}
	if _, err := runAgentFuncs(t, root, `write_meta box paseo 1 "2026-10-07 12:00"`); err == nil {
		t.Error("a malformed creation time must be rejected")
	}
}

func TestAgentReportsVMsInCreation(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vms", "half"), 0o700); err != nil {
		t.Fatal(err)
	}
	setup := `: > "$(vm_dir half)/creating"; write_meta half paseo 2; `
	out, err := runAgentFuncs(t, root, setup+`agent_busy() { return 0; }; vm_json half`)
	if err != nil || !strings.Contains(out, `"state":"creating"`) {
		t.Errorf("a VM whose creation is running must be reported as creating: %s, %v", out, err)
	}
	// nothing holds the agent lock: the marker is a leftover of a killed agent
	out, err = runAgentFuncs(t, root, setup+`agent_busy() { return 1; }; vm_json half`)
	if err != nil || !strings.Contains(out, `"state":"broken"`) {
		t.Errorf("a leftover must be reported as broken: %s, %v", out, err)
	}
}

func TestAgentGuardedRemoveChecksMetadata(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vms", "box"), 0o700); err != nil {
		t.Fatal(err)
	}
	stubs := `stop_vm() { :; }; remove_host_entry() { :; }; write_meta box paseo 1 2026-10-07T12:00:00Z; `
	for _, call := range []string{`cmd_rm box other 2026-10-07T12:00:00Z`, `cmd_rm box paseo 2026-10-07T12:00:01Z`, `cmd_rm box paseo -`} {
		if out, err := runAgentFuncs(t, root, stubs+call); err == nil {
			t.Errorf("%s must refuse a VM that changed since it was listed: %s", call, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "vms", "box")); err != nil {
		t.Fatal("a refused removal must keep the VM")
	}
	out, err := runAgentFuncs(t, root, stubs+`cmd_rm box paseo 2026-10-07T12:00:00Z`)
	if err != nil || out != "removed" {
		t.Errorf("matching removal: %q, %v", out, err)
	}
	out, err = runAgentFuncs(t, root, `cmd_rm box paseo 2026-10-07T12:00:00Z`)
	if err != nil || out != "gone" {
		t.Errorf("a VM someone else removed is not an error: %q, %v", out, err)
	}
}

func TestAgentValidatesNumbers(t *testing.T) {
	root := t.TempDir()
	if _, err := runAgentFuncs(t, root, `validate_int_range 4 1 32 vcpus; validate_disk 4G`); err != nil {
		t.Errorf("valid values rejected: %v", err)
	}
	for _, bad := range []string{`validate_int_range 0 1 32 vcpus`, `validate_int_range abc 1 32 vcpus`, `validate_int_range 33 1 32 vcpus`, `validate_disk 4g`, `validate_disk 1T`} {
		if _, err := runAgentFuncs(t, root, bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestAgentParseForward(t *testing.T) {
	root := t.TempDir()
	out, err := runAgentFuncs(t, root, `parse_forward 8080; parse_forward 3000:80; parse_forward 1024:1`)
	if err != nil || out != "8080 8080\n3000 80\n1024 1" {
		t.Errorf("parse_forward output %q, err %v", out, err)
	}
	for _, bad := range []string{"80", "70000", "8080:0", "a:b", "8080:", "80:80"} {
		if _, err := runAgentFuncs(t, root, `parse_forward "`+bad+`"`); err == nil {
			t.Errorf("parse_forward %q should fail", bad)
		}
	}
}

func TestAgentForwardBookkeeping(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	for _, name := range []string{"web", "db"} {
		if err := os.MkdirAll(filepath.Join(root, "vms", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runAgentFuncs(t, root, `
		save_forward web 8080 80; save_forward web 3000 3000; save_forward web 8080 81   # replaces 8080
		save_forward db 5432 5432
		cat "$CRACKLET_ROOT/vms/web/forwards" | tr '\n' ' '; echo
		forward_owner 5432; forward_owner 9999; echo "(none)"
		forwards_json web
		drop_forward web 3000; cat "$CRACKLET_ROOT/vms/web/forwards"`)
	if err != nil {
		t.Fatalf("bookkeeping failed: %v\n%s", err, out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("unexpected output:\n%s", out)
	}
	if strings.TrimSpace(lines[0]) != "3000:3000 8080:81" {
		t.Errorf("forwards file = %q", lines[0])
	}
	if lines[1] != "db" || lines[2] != "(none)" {
		t.Errorf("forward_owner = %q / %q", lines[1], lines[2])
	}
	if !strings.Contains(lines[3], `{"host":8080,"guest":81,"state":"active"}`) || !strings.Contains(lines[3], `{"host":3000,"guest":3000,"state":"inactive"}`) {
		t.Errorf("forwards_json = %s", lines[3])
	}
	if lines[4] != "8080:81" {
		t.Errorf("after drop_forward: %q", lines[4])
	}
}

// TestAgentHostNetworkIsolatesGuests drives setup_host_network with stubbed
// system tools and checks the firewall shape: guests must not reach each
// other, the Lima gateway (= macOS loopback), the LAN or services on the Lima
// VM itself, the rules must not rely on the default chain policies, and they
// must be installed atomically so running guests never see an empty chain.
func TestAgentHostNetworkIsolatesGuests(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	t.Setenv("SYSCTL_DROPIN", filepath.Join(root, "sysctl.conf"))
	out, err := runAgentFuncs(t, root, `
		iptables() { [[ " $* " == *" -C "* ]] && return 1; echo "iptables $*"; }
		iptables-restore() { echo "iptables-restore $*"; cat; }
		sysctl() { :; }
		ip() { echo '[{"dev":"eth0","gateway":"192.168.5.2"}]'; }
		setup_host_network`)
	if err != nil {
		t.Fatalf("setup_host_network failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "iptables -w -F") || strings.Contains(out, "iptables -w -A") {
		t.Errorf("chains must be rebuilt through iptables-restore, not flushed in place:\n%s", out)
	}
	for _, want := range []string{
		"iptables-restore --noflush -w",
		"*filter",
		":CRACKLET-FORWARD - [0:0]",
		":CRACKLET-INPUT - [0:0]",
		"-A CRACKLET-FORWARD -i cracklet+ ! -s 172.16.0.0/16 -j DROP",
		"-A CRACKLET-FORWARD -i cracklet+ -o cracklet+ -j DROP",
		"-A CRACKLET-FORWARD -i cracklet+ -d 192.168.5.2 -j REJECT",
		"-A CRACKLET-FORWARD -i cracklet+ -d 10.0.0.0/8 -j REJECT",
		"-A CRACKLET-FORWARD -i cracklet+ -d 172.16.0.0/12 -j REJECT",
		"-A CRACKLET-FORWARD -i cracklet+ -d 192.168.0.0/16 -j REJECT",
		"-A CRACKLET-FORWARD -i cracklet+ -d 100.64.0.0/10 -j REJECT",
		"-A CRACKLET-FORWARD -i cracklet+ -d 169.254.0.0/16 -j REJECT",
		"-A CRACKLET-FORWARD -i cracklet+ -d 127.0.0.0/8 -j REJECT",
		"-A CRACKLET-FORWARD -i cracklet+ -o eth0 -j ACCEPT",
		"-A CRACKLET-FORWARD -o cracklet+ -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"-A CRACKLET-FORWARD -i cracklet+ -j DROP",
		"-A CRACKLET-INPUT -i cracklet+ -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT",
		"-A CRACKLET-INPUT -i cracklet+ -j DROP",
		"COMMIT",
		"iptables -w -I INPUT -j CRACKLET-INPUT",
		"iptables -w -I FORWARD -j CRACKLET-FORWARD",
		"iptables -w -t nat -A POSTROUTING -s 172.16.0.0/16 ! -d 172.16.0.0/16 -j MASQUERADE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The terminal DROP must come after the ACCEPT rules of its chain.
	for chain, accept := range map[string]string{
		"CRACKLET-FORWARD": "-A CRACKLET-FORWARD -i cracklet+ -o eth0 -j ACCEPT",
		"CRACKLET-INPUT":   "-A CRACKLET-INPUT -i cracklet+ -m conntrack",
	} {
		a, d := strings.LastIndex(out, accept), strings.LastIndex(out, "-A "+chain+" -i cracklet+ -j DROP")
		if a < 0 || d < a {
			t.Errorf("%s: terminal DROP must follow the ACCEPT rules:\n%s", chain, out)
		}
	}
	// The hooks must be installed only after the chains carry their rules.
	if strings.Index(out, "COMMIT") > strings.Index(out, "-I FORWARD -j CRACKLET-FORWARD") {
		t.Errorf("hook into FORWARD must come after the atomic chain commit:\n%s", out)
	}
	if b, err := os.ReadFile(filepath.Join(root, "sysctl.conf")); err != nil || !strings.Contains(string(b), "ip_forward=1") {
		t.Errorf("sysctl drop-in not written: %v %q", err, b)
	}
}

// TestAgentCreateTapDisablesIPv6 ensures the IPv4-only firewall cannot be
// side-stepped over IPv6 link-local addresses on the tap.
func TestAgentCreateTapDisablesIPv6(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	root := t.TempDir()
	t.Setenv("SYSCTL_DROPIN", filepath.Join(root, "sysctl.conf"))
	out, err := runAgentFuncs(t, root, `
		iptables() { [[ " $* " == *" -C "* ]] && return 1; :; }
		iptables-restore() { cat >/dev/null; }
		sysctl() { echo "sysctl $*"; }
		ip() { if [[ $1 == -j ]]; then echo '[{"dev":"eth0","gateway":"192.168.5.2"}]'; else echo "ip $*"; fi; }
		create_tap 3`)
	if err != nil {
		t.Fatalf("create_tap failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"ip tuntap add dev cracklet3 mode tap",
		"sysctl -q -w net.ipv6.conf.cracklet3.disable_ipv6=1",
		"ip link set dev cracklet3 up",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "disable_ipv6=1") > strings.Index(out, "set dev cracklet3 up") {
		t.Errorf("IPv6 must be disabled before the tap comes up:\n%s", out)
	}
}

// TestAgentInstallMotdReplacesUbuntuBanner checks that the guest greets with a
// cracklet banner instead of Ubuntu's "minimized" and Landscape notices, and
// that the banner script runs even on a host without /proc or hostname -I.
func TestAgentInstallMotdReplacesUbuntuBanner(t *testing.T) {
	root := t.TempDir()
	motdDir := filepath.Join(root, "etc", "update-motd.d")
	if err := os.MkdirAll(motdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"00-header", "10-help-text", "50-landscape-sysinfo", "60-unminimize"} {
		if err := os.WriteFile(filepath.Join(motdDir, stale), []byte("#!/bin/sh\necho stale\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "legal"), []byte("legal notice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runAgentFuncs(t, root, `install_motd "`+root+`"`); err != nil {
		t.Fatalf("install_motd failed: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(motdDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "00-cracklet" {
		t.Fatalf("update-motd.d should only contain 00-cracklet, got %v", entries)
	}
	script := filepath.Join(motdDir, "00-cracklet")
	if info, err := os.Stat(script); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("banner script must be executable: %v %v", err, info)
	}
	if b, err := os.ReadFile(filepath.Join(root, "etc", "legal")); err != nil || len(b) != 0 {
		t.Errorf("/etc/legal should be emptied, got %v %q", err, b)
	}
	out, err := exec.Command("sh", script).CombinedOutput()
	if err != nil {
		t.Fatalf("banner script failed: %v\n%s", err, out)
	}
	host, _ := os.Hostname()
	for _, want := range []string{"cracklet", host, "ip", "vcpus", "memory", "up"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("banner missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "stale") {
		t.Errorf("Ubuntu banner scripts still run:\n%s", out)
	}
	if !strings.Contains(Script, "install_motd \"$root\"") {
		t.Error("customize_rootfs must call install_motd")
	}
}

func TestAgentProfileHelpers(t *testing.T) {
	root := t.TempDir()
	out, err := runAgentFuncs(t, root, `
		validate_profile base; validate_profile paseo
		profile_image base; profile_image paseo
		profile_disk_size base; profile_disk_size paseo
		golden_dir paseo 2 1024`)
	if err != nil {
		t.Fatalf("profile helpers failed: %v\n%s", err, out)
	}
	want := strings.Join([]string{
		root + "/images/rootfs-base.ext4", // unchanged so existing installs keep their image
		root + "/images/rootfs-paseo.ext4",
		"2G", "8G",
		root + "/images/golden-paseo-2-1024",
	}, "\n")
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	for _, bad := range []string{"Paseo", "", "../base", "node"} {
		if _, err := runAgentFuncs(t, root, `validate_profile "`+bad+`"`); err == nil {
			t.Errorf("profile %q accepted", bad)
		}
	}
}

// TestAgentRootfsStamp pins the base stamp to its pre-profile format, so an
// upgrade does not rebuild every existing base image, and ties each profile
// stamp to the base stamp so a new base forces the profile to follow.
func TestAgentRootfsStamp(t *testing.T) {
	root := t.TempDir()
	out, err := runAgentFuncs(t, root, `rootfs_stamp base 'u|s|rev7|e|k'; rootfs_stamp paseo 'u|s|rev7|e|k'`)
	if err != nil {
		t.Fatalf("rootfs_stamp failed: %v\n%s", err, out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 2 || lines[0] != "u|s|rev7|e|k" || !strings.HasPrefix(lines[1], "u|s|rev7|e|k|profile=paseo|rev") {
		t.Errorf("unexpected stamps:\n%s", out)
	}
}

func TestAgentPaseoProfileScript(t *testing.T) {
	root := t.TempDir()
	out, err := runAgentFuncs(t, root, `profile_script paseo`)
	if err != nil {
		t.Fatalf("profile_script failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"set -euo pipefail",
		"deb.nodesource.com/node_22.x",
		"cli.github.com/packages",
		"nodejs", " git ", " gh ", " rsync",
		"npm install -g @getpaseo/cli",
		"https://claude.ai/install.sh",
		"ln -sfn /root/.local/bin/claude /usr/local/bin/claude",
		"rm -rf /root/.paseo",
		"/var/lib/apt/lists/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("profile script missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "systemctl enable") || strings.Contains(out, "paseo daemon start") {
		t.Errorf("the profile must never start the paseo daemon at build time:\n%s", out)
	}
	if _, err := runAgentFuncs(t, root, `profile_script base`); err == nil {
		t.Error("base has no profile script")
	}
}

// TestAgentPaseoUnitInstalledButDisabled guards the golden snapshot: an
// enabled unit would start the daemon in the golden VM, and every clone would
// then share its keypair.
func TestAgentPaseoUnitInstalledButDisabled(t *testing.T) {
	root := t.TempDir()
	guest := filepath.Join(root, "guest")
	if out, err := runAgentFuncs(t, root, `install_paseo_unit "`+guest+`"`); err != nil {
		t.Fatalf("install_paseo_unit failed: %v\n%s", err, out)
	}
	unit, err := os.ReadFile(filepath.Join(guest, "etc", "systemd", "system", "paseo.service"))
	if err != nil {
		t.Fatalf("unit not written: %v", err)
	}
	for _, want := range []string{"ExecStart=/usr/bin/paseo daemon run --home /root/.paseo", "WantedBy=multi-user.target"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	links, _ := filepath.Glob(filepath.Join(guest, "etc", "systemd", "system", "*.wants", "paseo.service"))
	if len(links) != 0 {
		t.Errorf("paseo.service must not be enabled, found %v", links)
	}
}

// TestAgentProfileCurrent ensures `new --profile` refuses an image built from
// an older base (old cracklet-envd and authorized_keys).
func TestAgentProfileCurrent(t *testing.T) {
	root := t.TempDir()
	out, err := runAgentFuncs(t, root, `
		mkdir -p "$CRACKLET_ROOT/images"
		echo 'b1' > "$CRACKLET_ROOT/images/rootfs-base.stamp"
		rootfs_stamp paseo b1 > "$CRACKLET_ROOT/images/rootfs-paseo.stamp"
		profile_current paseo && echo current
		echo 'b2' > "$CRACKLET_ROOT/images/rootfs-base.stamp"
		profile_current paseo || echo stale
		rm "$CRACKLET_ROOT/images/rootfs-paseo.stamp"
		profile_current paseo || echo missing`)
	if err != nil || out != "current\nstale\nmissing" {
		t.Errorf("profile_current: %q, %v", out, err)
	}
}

// TestAgentReleaseStaleMountsWithoutLeftovers: a healthy run has nothing to
// unmount, and that must not abort the agent under set -e/pipefail.
func TestAgentReleaseStaleMountsWithoutLeftovers(t *testing.T) {
	root := t.TempDir()
	out, err := runAgentFuncs(t, root, `
		findmnt() { echo /; echo /dev; echo "$CRACKLET_ROOT/images/build.abc/root/dev"; }
		umount() { echo "umount $*"; }
		release_stale_mounts; echo done
		findmnt() { echo /; }
		release_stale_mounts; echo done`)
	if err != nil {
		t.Fatalf("release_stale_mounts failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "umount -R -l "+root+"/images/build.abc/root/dev") || strings.Count(out, "done") != 2 {
		t.Errorf("unexpected output:\n%s", out)
	}
	if strings.Contains(out, "umount -R -l /dev") {
		t.Errorf("must only touch build roots:\n%s", out)
	}
}
