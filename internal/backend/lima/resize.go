package lima

import (
	"context"
	"fmt"
	"strings"

	"github.com/itlabs-gmbh/skiff/internal/backend"
	"github.com/itlabs-gmbh/skiff/internal/config"
	limactl "github.com/itlabs-gmbh/skiff/internal/lima"
	"github.com/itlabs-gmbh/skiff/internal/vm"
)

// ResizeFor returns the changes the explicitly requested size makes to inst.
// Lima cannot shrink a disk, so that is rejected before the Lima VM is touched.
func ResizeFor(o backend.PrepareOptions, inst limactl.Instance) (limactl.Resize, error) {
	var r limactl.Resize
	if o.Explicit == (backend.SizeFlags{}) {
		return r, nil
	}
	if inst.Status != limactl.StatusRunning && inst.Status != limactl.StatusStopped {
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
			return limactl.Resize{}, fmt.Errorf("the Lima VM disk is %d GiB and cannot shrink to %d GiB; "+
				"recreate the VM with 'limactl delete -f %s' and 'skiff prepare --disk %d' instead",
				inst.Disk>>30, o.DiskGiB, config.Instance, o.DiskGiB)
		}
		r.DiskGiB = o.DiskGiB
	}
	return r, nil
}

// resizeInstance applies r, which needs a restart of the Lima VM. A restart
// kills every microVM, so running ones must be stopped by the user first, and
// the Lima lock keeps new ones from starting until the VM is back.
func (b *Backend) resizeInstance(ctx context.Context, inst limactl.Instance, r limactl.Resize) error {
	release, err := b.HoldForResize()
	if err != nil {
		return err
	}
	defer release()
	if inst.Status == limactl.StatusRunning {
		if err := b.refuseWhileMicroVMsRun(ctx); err != nil {
			return err
		}
		b.printf("==> Stopping Lima VM %q to resize it\n", config.Instance)
		if err := b.lima.Stop(ctx); err != nil {
			return err
		}
	}
	b.printf("==> Resizing Lima VM %q (%s)\n", config.Instance, describeResize(inst, r))
	if err := b.lima.Edit(ctx, r); err != nil {
		// A plain re-run would just start the VM at its old size.
		return fmt.Errorf("%w; the Lima VM is stopped, re-run prepare with the same size flags", err)
	}
	b.printf("==> Starting Lima VM %q\n", config.Instance)
	if err := b.lima.Start(ctx); err != nil {
		return fmt.Errorf("%w; the new size is saved, re-run 'skiff prepare' to start the Lima VM", err)
	}
	return nil
}

func (b *Backend) refuseWhileMicroVMsRun(ctx context.Context) error {
	vms, err := b.List(ctx)
	if err != nil {
		return fmt.Errorf("check for running microVMs before resizing: %w", err)
	}
	var running []string
	for _, v := range vms {
		if v.State == vm.StateRunning {
			running = append(running, v.Name)
		}
	}
	if len(running) > 0 {
		return fmt.Errorf("resizing restarts the Lima VM, which would kill the running microVMs %s; "+
			"stop them with 'skiff stop NAME' and run prepare again", strings.Join(running, ", "))
	}
	return nil
}

func describeResize(inst limactl.Instance, r limactl.Resize) string {
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
