package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuestImageHasPerVMHostKeysAndFirewall: the image must not ship the host
// keys apt generated at build time (every VM would share them and pinning
// would mean nothing), and it must carry the internet-only guard rail.
func TestGuestImageHasPerVMHostKeysAndFirewall(t *testing.T) {
	for _, want := range []string{
		"rm -f /etc/ssh/ssh_host_*",
		"COPY --chmod=0644 hostkeys.conf /etc/systemd/system/ssh.service.d/10-skiff-hostkeys.conf",
		"HostKey /etc/ssh/ssh_host_ed25519_key",
		"nftables",
		"COPY --chmod=0644 nftables.conf /etc/nftables.conf",
		"systemctl enable ssh systemd-networkd systemd-resolved nftables",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	if imageRev == "3" {
		t.Error("the Dockerfile changed, imageRev must be bumped")
	}
	for _, want := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16",
		"ct state established,related accept", "udp dport { 53, 67 } accept", "reject"} {
		if !strings.Contains(nftablesConf, want) {
			t.Errorf("nftables.conf lacks %q", want)
		}
	}
}

// TestGuestImageReplacesUbuntusLoginBanner: Ubuntu's update-motd scripts (the
// help links and the "minimized" notice) and the legal notice give way to
// skiff's own banner, which pam_motd runs on every ssh login.
func TestGuestImageReplacesUbuntusLoginBanner(t *testing.T) {
	for _, want := range []string{
		"rm -f /etc/update-motd.d/* /etc/motd",
		": > /etc/legal",
		"COPY --chmod=0755 motd /etc/update-motd.d/00-skiff",
	} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	if !strings.HasPrefix(motdScript, "#!/bin/sh\n") {
		t.Error("run-parts only runs the banner with a shebang")
	}
	for _, want := range []string{"skiff", "hostname -I", "nproc", "/proc/meminfo", "/proc/uptime", "no LAN"} {
		if !strings.Contains(motdScript, want) {
			t.Errorf("banner lacks %q", want)
		}
	}
	if strings.Contains(motdScript, "Firecracker") {
		t.Error("the banner must not name the removed Firecracker backend")
	}
}

func TestPrepareWritesTheFirewallIntoTheBuildContext(t *testing.T) {
	f := newFake()
	var contextDir string
	f.onBuild = func(dir string) { contextDir = dir }
	b, _, _ := newTestBackend(t, f)
	// Keep the context around: ensureImage removes it after the build.
	f.labels = nil
	if err := b.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if contextDir == "" {
		t.Fatal("build never ran")
	}
	if !strings.Contains(hostKeysDropIn, "ssh-keygen -q -N \"\" -t ed25519") || !strings.Contains(hostKeysDropIn, "ExecStartPre=/usr/sbin/sshd -t") {
		t.Errorf("drop-in must make the key before sshd's config check:\n%s", hostKeysDropIn)
	}
	for name, want := range map[string]string{"nftables.conf": nftablesConf, "hostkeys.conf": hostKeysDropIn, "motd": motdScript, "authorized_keys": "ssh-ed25519 AAAA test\n"} {
		if got, ok := f.built[name]; !ok || got != want {
			t.Errorf("%s in the build context = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(contextDir, "Dockerfile")); err == nil {
		t.Error("the build context must be removed after the build")
	}
}
