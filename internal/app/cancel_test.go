package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// TestInterruptedAgentProbeDoesNotRemoveExistingVM: Ctrl-C while the agent
// checksum is probed must not count as an interrupted creation. Before the
// probe moved into the backend it ran ahead of `started`; a VM of the same
// name that already exists would otherwise be removed, disk included.
func TestInterruptedAgentProbeDoesNotRemoveExistingVM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := defaultHandler(map[string]string{"rm": ""})
	probed := false
	a, fake, _ := newTestApp(t, func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		if strings.Contains(joined, "sha256sum") && !probed {
			probed = true
			cancel() // the user hits Ctrl-C while limactl probes the agent
			return nil, context.Canceled
		}
		return base(name, args) // a later probe (the cleanup's own context) succeeds
	})
	// An empty grant store: nothing makes NewVM list the VMs (and probe the
	// agent) before it would count the creation as started.
	_, err := a.NewVM(ctx, vm.Spec{Name: "vm1", VCPUs: 2, MemMiB: 1024, Disk: "2G"})
	if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("want the interrupted probe to fail, got %v", err)
	}
	if fake.CalledWithSuffix(config.AgentPath + " rm vm1") {
		t.Errorf("an existing vm1 must survive an interrupted probe:\n%s", fake.Dump())
	}
	if fake.CalledWithSuffix(" new vm1 2 1024 2G snapshot base") {
		t.Errorf("the agent must not be asked to create anything:\n%s", fake.Dump())
	}
}
