package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/envdbin"
	"github.com/itlabs-gmbh/cracklet/internal/host"
	"github.com/itlabs-gmbh/cracklet/internal/lima"
)

// PrepareOptions size the Lima VM created by Prepare.
type PrepareOptions struct {
	CPUs      int
	MemoryGiB int
	DiskGiB   int
}

func (o PrepareOptions) template() lima.TemplateOptions {
	return lima.TemplateOptions{CPUs: o.CPUs, MemoryGiB: o.MemoryGiB, DiskGiB: o.DiskGiB}
}

func (o PrepareOptions) isDefault() bool {
	return o == PrepareOptions{CPUs: config.DefaultLimaCPUs, MemoryGiB: config.DefaultLimaMemoryGiB, DiskGiB: config.DefaultLimaDiskGiB}
}

// Prepare makes the host ready: preflight, Lima instance, agent, images, ssh config.
// Every step is idempotent so the command can be re-run after a failure.
func (a *App) Prepare(ctx context.Context, o PrepareOptions) error {
	if err := o.template().Validate(); err != nil {
		return err
	}
	if err := a.preflight(ctx); err != nil {
		return err
	}
	if err := a.ensureKeyPair(ctx); err != nil {
		return err
	}
	if err := a.ensureInstance(ctx, o); err != nil {
		return err
	}
	if err := a.ensureAgent(ctx); err != nil {
		return err
	}
	if err := a.pushKeys(ctx); err != nil {
		return err
	}
	if err := a.pushEnvd(ctx); err != nil {
		return err
	}
	a.printf("==> Installing Firecracker %s, building guest images and the golden snapshot (this can take a few minutes)\n", config.FirecrackerVersion)
	if err := a.agentRun(ctx, "prepare",
		config.FirecrackerVersion, config.FirecrackerSHA256,
		config.KernelURL, config.KernelSHA256,
		config.RootfsURL, config.RootfsSHA256,
		strconv.Itoa(config.DefaultVCPUs), strconv.Itoa(config.DefaultMemMiB)); err != nil {
		return err
	}
	if err := a.writeSSHConfig(); err != nil {
		return err
	}
	a.printf("\nReady. Next steps:\n  cracklet new            # boot a microVM\n  cracklet ssh vm1        # open a shell\n\n"+
		"Optional, to use plain 'ssh vm1.cracklet', add this line to ~/.ssh/config:\n  Include %s\n", a.paths.SSHConfigPath())
	return nil
}

// Doctor prints the preflight result without changing anything.
func (a *App) Doctor(ctx context.Context) error {
	facts := a.gatherFacts(ctx)
	a.printf("macOS %s on %s (%s/%s), hypervisor=%v, brew=%v, lima=%v\n",
		facts.MacOSVersion, facts.Chip, facts.OS, facts.Arch, facts.HVSupport, facts.HasBrew, facts.HasLima)
	problems := host.Check(facts)
	for _, p := range problems {
		a.printf("  [%s] %s\n", severity(p), p.Message)
	}
	if host.HasFatal(problems) {
		return fmt.Errorf("host is not ready for cracklet")
	}
	if !facts.HasLima {
		a.printf("Lima instance %q: Lima is not installed yet (run 'cracklet prepare')\n", config.Instance)
		return nil
	}
	inst, ok, err := a.lima.Get(ctx)
	switch {
	case err != nil:
		return err
	case !ok:
		a.printf("Lima instance %q: not created (run 'cracklet prepare')\n", config.Instance)
	default:
		a.printf("Lima instance %q: %s\n", config.Instance, inst.Status)
	}
	return nil
}

func (a *App) preflight(ctx context.Context) error {
	a.printf("==> Checking host\n")
	facts := a.gatherFacts(ctx)
	problems := host.Check(facts)
	for _, p := range problems {
		a.printf("  [%s] %s\n", severity(p), p.Message)
	}
	if host.HasFatal(problems) {
		return fmt.Errorf("host is not ready for cracklet")
	}
	if !facts.HasLima {
		a.printf("==> Installing Lima\n")
		if err := a.r.Run(ctx, "brew", "install", "lima"); err != nil {
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

func (a *App) ensureKeyPair(ctx context.Context) error {
	_, err := os.Stat(a.paths.KeyPath())
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check ssh key: %w", err)
	}
	if err := os.MkdirAll(a.paths.Home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", a.paths.Home, err)
	}
	a.printf("==> Generating SSH key %s\n", a.paths.KeyPath())
	if err := a.r.Run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "cracklet", "-f", a.paths.KeyPath()); err != nil {
		return fmt.Errorf("generate ssh key: %w", err)
	}
	return nil
}

func (a *App) ensureInstance(ctx context.Context, o PrepareOptions) error {
	inst, ok, err := a.lima.Get(ctx)
	if err != nil {
		return err
	}
	if ok && !o.isDefault() {
		a.printf("  note: --cpus/--memory/--disk only apply when the Lima VM is created; "+
			"delete it with 'limactl delete -f %s' to resize\n", config.Instance)
	}
	switch {
	case !ok:
		a.printf("==> Creating Lima VM %q (%d CPUs, %d GiB RAM, %d GiB disk, nested virtualization)\n",
			config.Instance, o.CPUs, o.MemoryGiB, o.DiskGiB)
		text, err := lima.RenderTemplate(o.template())
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(a.paths.LimaTemplatePath()), 0o700); err != nil {
			return fmt.Errorf("create %s: %w", a.paths.Home, err)
		}
		if err := os.WriteFile(a.paths.LimaTemplatePath(), []byte(text), 0o600); err != nil {
			return fmt.Errorf("write lima template: %w", err)
		}
		return a.lima.Create(ctx, a.paths.LimaTemplatePath())
	case inst.Status != lima.StatusRunning:
		a.printf("==> Starting Lima VM %q\n", config.Instance)
		return a.lima.Start(ctx)
	default:
		a.printf("==> Lima VM %q is running\n", config.Instance)
		return nil
	}
}

// pushEnvd ships the embedded guest daemon; the agent bakes it into the base image.
func (a *App) pushEnvd(ctx context.Context) error {
	if len(envdbin.Binary) < 1024 {
		return fmt.Errorf("embedded cracklet-envd binary is missing; build cracklet with 'make build'")
	}
	return a.lima.WriteFile(ctx, bytes.NewReader(envdbin.Binary), config.GuestEnvdPath, "0755")
}

func (a *App) pushKeys(ctx context.Context) error {
	for _, f := range []struct{ src, dst, mode string }{
		{a.paths.KeyPath(), config.GuestKeyPath, "0600"},
		{a.paths.PubKeyPath(), config.GuestKeyPath + ".pub", "0644"},
	} {
		data, err := os.ReadFile(f.src)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.src, err)
		}
		if err := a.lima.WriteFile(ctx, strings.NewReader(string(data)), f.dst, f.mode); err != nil {
			return err
		}
	}
	return nil
}
