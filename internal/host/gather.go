package host

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// probeTimeout bounds each system query so a hung tool cannot stall cracklet.
const probeTimeout = 15 * time.Second

// LookPathFunc resolves an executable name; it mirrors exec.LookPath.
type LookPathFunc func(name string) (string, error)

// Gatherer collects Facts about the host.
type Gatherer struct {
	Runner   runner.Runner
	LookPath LookPathFunc
}

// Gather collects host facts by querying the system.
func Gather(ctx context.Context, r runner.Runner) Facts {
	return Gatherer{Runner: r, LookPath: exec.LookPath}.Gather(ctx)
}

// Gather collects host facts using the configured runner and path lookup.
func (g Gatherer) Gather(ctx context.Context) Facts {
	f := Facts{OS: runtime.GOOS, Arch: runtime.GOARCH}
	f.HasBrew = g.has("brew")
	f.HasLima = g.has("limactl")
	if f.OS != "darwin" {
		return f
	}
	f.MacOSVersion = g.output(ctx, "sw_vers", "-productVersion")
	f.Chip = g.output(ctx, "sysctl", "-n", "machdep.cpu.brand_string")
	f.HVSupport = g.output(ctx, "sysctl", "-n", "kern.hv_support") == "1"
	return f
}

func (g Gatherer) has(name string) bool {
	_, err := g.LookPath(name)
	return err == nil
}

func (g Gatherer) output(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := g.Runner.Output(ctx, name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
