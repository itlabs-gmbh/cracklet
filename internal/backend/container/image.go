package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// ImageRef is the guest image every machine boots from.
	ImageRef = "cracklet/guest:latest"
	// imageRev is bumped whenever the Dockerfile changes.
	imageRev = "2"
	labelRev = "cracklet.rev"
	labelKey = "cracklet.key"
)

// dockerfile is the guest image: Ubuntu with systemd as PID 1 (the machine
// init execs /sbin/init), sshd for root with cracklet's key, networkd DHCP so
// the guest takes the address `machine ls` reports, and the tools the
// provisioning scripts (internal/guest) rely on. The key is copied from the
// build context, never interpolated into a shell line. Only the labels are
// formatted in, and they are hex and a constant.
const dockerfile = `FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      systemd systemd-sysv systemd-resolved openssh-server sudo curl ca-certificates \
      git jq iproute2 iputils-ping less vim-tiny \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /run/sshd \
    && systemctl enable ssh systemd-networkd systemd-resolved
RUN printf '[Match]\nName=e*\n\n[Network]\nDHCP=yes\n' > /etc/systemd/network/10-eth.network \
    && printf 'PermitRootLogin prohibit-password\n' > /etc/ssh/sshd_config.d/10-cracklet.conf \
    && install -d -m 0700 /root/.ssh
COPY --chmod=0600 authorized_keys /root/.ssh/authorized_keys
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
	if key == "" || strings.ContainsAny(key, "\n\r\x00") {
		return fmt.Errorf("public key %s must be a single line", b.paths.PubKeyPath())
	}
	want := imageLabels([]byte(key))
	have, err := b.imageLabels(ctx)
	switch {
	case err == nil && have[labelRev] == want[labelRev] && have[labelKey] == want[labelKey]:
		b.printf("==> Guest image %s is current\n", ImageRef)
		return nil
	case err != nil && !errors.Is(err, errNoImage):
		return err
	}
	dir, err := os.MkdirTemp("", "cracklet-image-")
	if err != nil {
		return fmt.Errorf("create build context: %w", err)
	}
	defer os.RemoveAll(dir)
	content := fmt.Sprintf(dockerfile, labelRev, want[labelRev], labelKey, want[labelKey])
	for name, data := range map[string]string{"Dockerfile": content, "authorized_keys": key + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	b.printf("==> Building guest image %s (Ubuntu 24.04 with systemd and sshd; a few minutes the first time)\n", ImageRef)
	if err := b.r.Run(ctx, "container", "build", "--tag", ImageRef, dir); err != nil {
		return fmt.Errorf("build guest image: %w", err)
	}
	return nil
}

// errNoImage means the guest image is not installed (as opposed to the
// service being unreachable, which must not trigger a rebuild).
var errNoImage = errors.New("guest image not found")

// imageLabels reads the labels of the installed guest image.
func (b *Backend) imageLabels(ctx context.Context) (map[string]string, error) {
	out, err := b.run(ctx, "image", "inspect", ImageRef)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil, errNoImage
		}
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
		return nil, errNoImage
	}
	return images[0].Variants[0].Config.Config.Labels, nil
}
