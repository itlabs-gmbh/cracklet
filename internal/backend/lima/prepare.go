package lima

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/host"
	limactl "github.com/itlabs-gmbh/cracklet/internal/lima"
	"github.com/itlabs-gmbh/cracklet/internal/sshkey"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

func template(o backend.PrepareOptions) limactl.TemplateOptions {
	return limactl.TemplateOptions{CPUs: o.CPUs, MemoryGiB: o.MemoryGiB, DiskGiB: o.DiskGiB}
}

// extraProfiles validates the requested profiles and returns them without
// duplicates and without base, which the agent always builds.
func extraProfiles(o backend.PrepareOptions) ([]string, error) {
	extra := make([]string, 0, len(o.Profiles))
	seen := map[string]bool{vm.ProfileBase: true}
	for _, p := range o.Profiles {
		if err := vm.ValidateProfile(p); err != nil {
			return nil, err
		}
		if !seen[p] {
			seen[p] = true
			extra = append(extra, p)
		}
	}
	return extra, nil
}

// Prepare implements backend.Backend: preflight, Lima instance, agent, keys,
// images. Every step is idempotent so the command can be re-run after a failure.
func (b *Backend) Prepare(ctx context.Context, o backend.PrepareOptions) error {
	if err := template(o).Validate(); err != nil {
		return err
	}
	profiles, err := extraProfiles(o)
	if err != nil {
		return err
	}
	if err := b.preflight(ctx); err != nil {
		return err
	}
	if err := sshkey.Ensure(ctx, b.r, b.paths, b.out); err != nil {
		return err
	}
	if err := b.ensureInstance(ctx, o); err != nil {
		return err
	}
	if err := b.ensureAgent(ctx); err != nil {
		return err
	}
	if err := b.pushKeys(ctx); err != nil {
		return err
	}
	if err := b.pushEnvd(ctx); err != nil {
		return err
	}
	// Building a golden snapshot boots a microVM, so it must not overlap a resize.
	release, err := b.HoldForMicroVM()
	if err != nil {
		return err
	}
	defer release()
	b.printf("==> Installing Firecracker %s, building guest images and the golden snapshot (this can take a few minutes)\n", config.FirecrackerVersion)
	args := append([]string{"prepare",
		config.FirecrackerVersion, config.FirecrackerSHA256,
		config.KernelURL, config.KernelSHA256,
		config.RootfsURL, config.RootfsSHA256,
		strconv.Itoa(config.DefaultVCPUs), strconv.Itoa(config.DefaultMemMiB)}, profiles...)
	return b.agentRun(ctx, args...)
}

// Doctor implements backend.Backend.
func (b *Backend) Doctor(ctx context.Context) error {
	facts := b.gather(ctx)
	b.printf("macOS %s on %s (%s/%s), hypervisor=%v, brew=%v, lima=%v\n",
		facts.MacOSVersion, facts.Chip, facts.OS, facts.Arch, facts.HVSupport, facts.HasBrew, facts.HasLima)
	problems := host.Check(facts)
	for _, p := range problems {
		b.printf("  [%s] %s\n", severity(p), p.Message)
	}
	if host.HasFatal(problems) {
		return fmt.Errorf("host is not ready for cracklet")
	}
	if !facts.HasLima {
		b.printf("Lima instance %q: Lima is not installed yet (run 'cracklet prepare')\n", config.Instance)
		return nil
	}
	inst, ok, err := b.lima.Get(ctx)
	switch {
	case err != nil:
		return err
	case !ok:
		b.printf("Lima instance %q: not created (run 'cracklet prepare')\n", config.Instance)
	default:
		b.printf("Lima instance %q: %s\n", config.Instance, inst.Status)
	}
	return nil
}

func (b *Backend) preflight(ctx context.Context) error {
	b.printf("==> Checking host\n")
	facts := b.gather(ctx)
	problems := host.Check(facts)
	for _, p := range problems {
		b.printf("  [%s] %s\n", severity(p), p.Message)
	}
	if host.HasFatal(problems) {
		return fmt.Errorf("host is not ready for cracklet")
	}
	if !facts.HasLima {
		b.printf("==> Installing Lima\n")
		if err := b.r.Run(ctx, "brew", "install", "lima"); err != nil {
			return fmt.Errorf("install lima: %w", err)
		}
	}
	return nil
}

func severity(p host.Problem) string {
	if p.Fatal {
		return "ERROR"
	}
	return "WARN"
}

func (b *Backend) ensureInstance(ctx context.Context, o backend.PrepareOptions) error {
	inst, ok, err := b.lima.Get(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return b.createInstance(ctx, o)
	}
	r, err := ResizeFor(o, inst)
	if err != nil {
		return err
	}
	switch {
	case r != limactl.Resize{}:
		return b.resizeInstance(ctx, inst, r)
	case inst.Status != limactl.StatusRunning:
		b.printf("==> Starting Lima VM %q\n", config.Instance)
		return b.lima.Start(ctx)
	default:
		b.printf("==> Lima VM %q is running\n", config.Instance)
		return nil
	}
}

func (b *Backend) createInstance(ctx context.Context, o backend.PrepareOptions) error {
	b.printf("==> Creating Lima VM %q (%d CPUs, %d GiB RAM, %d GiB disk, nested virtualization)\n",
		config.Instance, o.CPUs, o.MemoryGiB, o.DiskGiB)
	text, err := limactl.RenderTemplate(template(o))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.paths.LimaTemplatePath()), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", b.paths.Home, err)
	}
	if err := os.WriteFile(b.paths.LimaTemplatePath(), []byte(text), 0o600); err != nil {
		return fmt.Errorf("write lima template: %w", err)
	}
	return b.lima.Create(ctx, b.paths.LimaTemplatePath())
}

// pushEnvd ships the guest daemon; the agent bakes it into the base image.
func (b *Backend) pushEnvd(ctx context.Context) error {
	data, err := b.envd(ctx)
	if err != nil {
		return err
	}
	return b.lima.WriteFile(ctx, bytes.NewReader(data), config.GuestEnvdPath, "0755")
}

func (b *Backend) pushKeys(ctx context.Context) error {
	for _, f := range []struct{ src, dst, mode string }{
		{b.paths.KeyPath(), config.GuestKeyPath, "0600"},
		{b.paths.PubKeyPath(), config.GuestKeyPath + ".pub", "0644"},
	} {
		data, err := os.ReadFile(f.src)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.src, err)
		}
		if err := b.lima.WriteFile(ctx, strings.NewReader(string(data)), f.dst, f.mode); err != nil {
			return err
		}
	}
	return nil
}
