package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptEmbedded(t *testing.T) {
	if !strings.HasPrefix(Script, "#!/usr/bin/env bash") {
		t.Fatalf("agent script should start with a bash shebang, got %q", firstLine(Script))
	}
	for _, cmd := range []string{"prepare", "new", "start", "stop", "rm", "ls", "forward", "unforward"} {
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
