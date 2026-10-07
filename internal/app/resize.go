package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/lima"
)

// SizeFlags marks the sizing options the user set explicitly. Only those are
// applied to an existing Lima VM, so re-running prepare with the defaults
// never undoes an earlier resize.
type SizeFlags struct {
	CPUs, Memory, Disk bool
}

// resizeFor returns the changes the explicitly requested size makes to inst.
// Lima cannot shrink a disk, so that is rejected before the Lima VM is touched.
func (o PrepareOptions) resizeFor(inst lima.Instance) (lima.Resize, error) {
	var r lima.Resize
	if o.Explicit == (SizeFlags{}) {
		return r, nil
	}
	if inst.Status != lima.StatusRunning && inst.Status != lima.StatusStopped {
		return r, fmt.Errorf("the Lima VM is %s and can only be resized when running or stopped; "+
			"check 'limactl list' and 'limactl stop -f %s'", inst.Status, config.Instance)
	}
	if inst.CPUs == 0 || inst.Memory == 0 || inst.Disk == 0 {
		return r, fmt.Errorf("limactl reports no size for the Lima VM %q, so it cannot be resized safely", config.Instance)
	}
	if o.Explicit.CPUs && o.CPUs != inst.CPUs {
		r.CPUs = o.CPUs
	}
	if o.Explicit.Memory && gibBytes(o.MemoryGiB) != inst.Memory {
		r.MemoryGiB = o.MemoryGiB
	}
	if o.Explicit.Disk && gibBytes(o.DiskGiB) != inst.Disk {
		if gibBytes(o.DiskGiB) < inst.Disk {
			return lima.Resize{}, fmt.Errorf("the Lima VM disk is %d GiB and cannot shrink to %d GiB; "+
				"recreate the VM with 'limactl delete -f %s' and 'cracklet prepare --disk %d' instead",
				inst.Disk>>30, o.DiskGiB, config.Instance, o.DiskGiB)
		}
		r.DiskGiB = o.DiskGiB
	}
	return r, nil
}

// resizeInstance applies r, which needs a restart of the Lima VM. A restart
// kills every microVM, so running ones must be stopped by the user first, and
// the Lima lock keeps new ones from starting until the VM is back.
func (a *App) resizeInstance(ctx context.Context, inst lima.Instance, r lima.Resize) error {
	release, err := a.holdLimaForResize()
	if err != nil {
		return err
	}
	defer release()
	if inst.Status == lima.StatusRunning {
		if err := a.refuseWhileMicroVMsRun(ctx); err != nil {
			return err
		}
		a.printf("==> Stopping Lima VM %q to resize it\n", config.Instance)
		if err := a.lima.Stop(ctx); err != nil {
			return err
		}
	}
	a.printf("==> Resizing Lima VM %q (%s)\n", config.Instance, describeResize(inst, r))
	if err := a.lima.Edit(ctx, r); err != nil {
		// A plain re-run would just start the VM at its old size.
		return fmt.Errorf("%w; the Lima VM is stopped, re-run prepare with the same size flags", err)
	}
	a.printf("==> Starting Lima VM %q\n", config.Instance)
	if err := a.lima.Start(ctx); err != nil {
		return fmt.Errorf("%w; the new size is saved, re-run 'cracklet prepare' to start the Lima VM", err)
	}
	return nil
}

func (a *App) refuseWhileMicroVMsRun(ctx context.Context) error {
	vms, err := a.ListVMs(ctx)
	if err != nil {
		return fmt.Errorf("check for running microVMs before resizing: %w", err)
	}
	var running []string
	for _, v := range vms {
		if v.State == "running" {
			running = append(running, v.Name)
		}
	}
	if len(running) > 0 {
		return fmt.Errorf("resizing restarts the Lima VM, which would kill the running microVMs %s; "+
			"stop them with 'cracklet stop NAME' and run prepare again", strings.Join(running, ", "))
	}
	return nil
}

func describeResize(inst lima.Instance, r lima.Resize) string {
	var parts []string
	if r.CPUs != 0 {
		parts = append(parts, fmt.Sprintf("%d → %d CPUs", inst.CPUs, r.CPUs))
	}
	if r.MemoryGiB != 0 {
		parts = append(parts, fmt.Sprintf("%d → %d GiB RAM", inst.Memory>>30, r.MemoryGiB))
	}
	if r.DiskGiB != 0 {
		parts = append(parts, fmt.Sprintf("%d → %d GiB disk", inst.Disk>>30, r.DiskGiB))
	}
	return strings.Join(parts, ", ")
}

func gibBytes(gib int) int64 { return int64(gib) << 30 }
