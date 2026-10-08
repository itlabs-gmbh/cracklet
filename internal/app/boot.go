package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/lima"
)

// errLimaStopped marks a Lima instance that requireRunning may start.
var errLimaStopped = errors.New("stopped")

// requireRunning makes sure the Lima instance is up. A stopped one, as after
// a reboot of the Mac, is started, and the microVMs that ran before it
// stopped come back; anything else needs 'cracklet prepare'.
func (a *App) requireRunning(ctx context.Context) error {
	if err := a.checkRunning(ctx); !errors.Is(err, errLimaStopped) {
		return err
	}
	return a.bootLima(ctx)
}

// checkRunning fails unless the Lima instance is up, without starting it:
// background polling must not undo a deliberate 'limactl stop'.
func (a *App) checkRunning(ctx context.Context) error {
	inst, ok, err := a.lima.Get(ctx)
	switch {
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("Lima instance %q does not exist yet, run 'cracklet prepare' first", config.Instance)
	case inst.Status == lima.StatusRunning:
		return nil
	case inst.Status == lima.StatusStopped:
		return fmt.Errorf("Lima instance %q is %w, run 'cracklet prepare' to start it", config.Instance, errLimaStopped)
	}
	return fmt.Errorf("Lima instance %q is %s, run 'cracklet prepare' to start it", config.Instance, inst.Status)
}

// bootLima starts the stopped Lima VM and waits until its microVMs are back.
func (a *App) bootLima(ctx context.Context) error {
	// A resize stops the Lima VM on purpose; it must not be started under it.
	hold, err := a.holdLimaForMicroVM()
	if err != nil {
		return err
	}
	defer hold()
	release, err := a.waitForLimaBoot(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Another cracklet command may have started it while this one waited.
	inst, ok, err := a.lima.Get(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Lima instance %q was removed, run 'cracklet prepare' to create it", config.Instance)
	}
	if inst.Status != lima.StatusRunning {
		a.statusf("==> Starting Lima VM %q\n", config.Instance)
		if err := a.lima.Start(ctx); err != nil {
			return err
		}
	}
	// The Lima VM is up, so the command itself can go on; a VM that is not
	// back yet is reported and can be started by hand.
	if err := a.restoreMicroVMs(ctx); err != nil {
		a.statusf("  [WARN] the Lima VM is running again, but restarting its microVMs failed: %v\n"+
			"         check 'cracklet ls' and start them with 'cracklet start NAME'\n", err)
	}
	return nil
}

// restoreResult is what `agent restore` reports.
type restoreResult struct {
	Started []string `json:"started"`
	Failed  []string `json:"failed"`
}

// restoreMicroVMs boots the microVMs that were running when the Lima VM
// stopped. The agent does so once per boot, so a second call only waits for
// the first (possibly the agent's boot unit) to finish.
func (a *App) restoreMicroVMs(ctx context.Context) error {
	if err := a.ensureAgent(ctx); err != nil {
		return err
	}
	out, err := a.agentOutput(ctx, "restore")
	if err != nil {
		return err
	}
	var r restoreResult
	if err := json.Unmarshal(out, &r); err != nil {
		return fmt.Errorf("parse agent output %q: %w", string(out), err)
	}
	if len(r.Started) > 0 {
		a.statusf("==> Restarted microVMs: %s\n", strings.Join(r.Started, ", "))
	}
	for _, name := range r.Failed {
		a.statusf("  [WARN] %s did not come back; check 'cracklet inspect %s' and run 'cracklet start %s'\n", name, name, name)
	}
	return nil
}
