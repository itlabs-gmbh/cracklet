package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ImageRef is the guest image every machine boots from; `machine inspect`
	// reports it, which is how List tells cracklet's machines apart.
	ImageRef = "cracklet/guest:latest"
	// imageRev is bumped whenever the Dockerfile changes.
	imageRev = "1"
	labelRev = "cracklet.rev"
	labelKey = "cracklet.key"
)

// dockerfile is the guest image: Ubuntu with systemd as PID 1 (the machine
// init execs /sbin/init), sshd for root with cracklet's key, networkd DHCP so
// the guest takes the address `machine ls` reports, and the tools the
// provisioning scripts (internal/guest) rely on.
const dockerfile = `FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      systemd systemd-sysv systemd-resolved openssh-server sudo curl ca-certificates \
      git jq iproute2 iputils-ping less vim-tiny \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /run/sshd \
    && systemctl enable ssh systemd-networkd systemd-resolved
RUN printf '[Match]\nName=e*\n\n[Network]\nDHCP=yes\n' > /etc/systemd/network/10-eth.network \
    && printf 'PermitRootLogin prohibit-password\n' > /etc/ssh/sshd_config.d/10-cracklet.conf
RUN install -d -m 0700 /root/.ssh && echo '%s' > /root/.ssh/authorized_keys \
    && chmod 0600 /root/.ssh/authorized_keys
LABEL %s=%s %s=%s
`

// imageLabels are the labels the current key and Dockerfile revision produce.
func imageLabels(pubkey []byte) map[string]string {
	sum := sha256.Sum256(pubkey)
	return map[string]string{labelRev: imageRev, labelKey: hex.EncodeToString(sum[:])}
}

// ensureImage builds the guest image unless one with the current labels exists.
func (b *Backend) ensureImage(ctx context.Context) error {
	pubkey, err := os.ReadFile(b.paths.PubKeyPath())
	if err != nil {
		return fmt.Errorf("read %s: %w", b.paths.PubKeyPath(), err)
	}
	key := strings.TrimSpace(string(pubkey))
	if strings.ContainsAny(key, "'\n") {
		return fmt.Errorf("public key %s has unexpected characters", b.paths.PubKeyPath())
	}
	want := imageLabels([]byte(key))
	have, err := b.imageLabels(ctx)
	if err == nil && have[labelRev] == want[labelRev] && have[labelKey] == want[labelKey] {
		b.printf("==> Guest image %s is current\n", ImageRef)
		return nil
	}
	dir, err := os.MkdirTemp("", "cracklet-image-")
	if err != nil {
		return fmt.Errorf("create build context: %w", err)
	}
	defer os.RemoveAll(dir)
	content := fmt.Sprintf(dockerfile, key, labelRev, want[labelRev], labelKey, want[labelKey])
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(content), 0o600); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}
	b.printf("==> Building guest image %s (Ubuntu 24.04 with systemd and sshd; a few minutes the first time)\n", ImageRef)
	if err := b.r.Run(ctx, "container", "build", "--tag", ImageRef, dir); err != nil {
		return fmt.Errorf("build guest image: %w", err)
	}
	return nil
}

// imageLabels reads the labels of the installed guest image.
func (b *Backend) imageLabels(ctx context.Context) (map[string]string, error) {
	out, err := b.run(ctx, "image", "inspect", ImageRef)
	if err != nil {
		return nil, err
	}
	var images []struct {
		Variants []struct {
			Config struct {
				Config struct {
					Labels map[string]string `json:"Labels"`
				} `json:"config"`
			} `json:"config"`
		} `json:"variants"`
	}
	if err := json.Unmarshal(out, &images); err != nil {
		return nil, fmt.Errorf("parse image inspect output: %w", err)
	}
	if len(images) == 0 || len(images[0].Variants) == 0 {
		return nil, fmt.Errorf("image %s has no variants", ImageRef)
	}
	return images[0].Variants[0].Config.Config.Labels, nil
}
