// Package lima wraps the limactl CLI.
package lima

import (
	"context"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// listTimeout bounds the quick status probe so a wedged limactl cannot hang cracklet.
const listTimeout = 30 * time.Second

var modeRe = regexp.MustCompile(`^[0-7]{3,4}$`)

// writeScript receives PATH and MODE as positional parameters, so neither is
// ever interpolated into shell syntax. Content lands in a per-process temp
// file (restrictive umask) and is renamed into place, so concurrent writers
// cannot trample each other and the file is never world-readable.
const writeScript = `mkdir -p -- "$(dirname -- "$1")" && umask 077 && t="$1.tmp.$$" && cat > "$t" && chmod "$2" "$t" && mv -f "$t" "$1"`

// Client drives a single Lima instance.
type Client struct {
	r        runner.Runner
	Instance string
}

// NewClient returns a Client for the named instance.
func NewClient(r runner.Runner, instance string) *Client {
	return &Client{r: r, Instance: instance}
}

// Get looks the instance up; ok is false when it does not exist.
func (c *Client) Get(ctx context.Context) (Instance, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	out, err := c.r.Output(ctx, "limactl", "list", "--format", "json")
	if err != nil {
		return Instance{}, false, fmt.Errorf("list lima instances: %w", err)
	}
	list, err := ParseList(out)
	if err != nil {
		return Instance{}, false, err
	}
	inst, ok := Find(list, c.Instance)
	return inst, ok, nil
}

// Create builds and boots the instance from a template file.
func (c *Client) Create(ctx context.Context, templatePath string) error {
	if err := c.r.Run(ctx, "limactl", "start", "--tty=false", "--name", c.Instance, templatePath); err != nil {
		return fmt.Errorf("create lima instance: %w", err)
	}
	return nil
}

// Start boots an existing, stopped instance.
func (c *Client) Start(ctx context.Context) error {
	if err := c.r.Run(ctx, "limactl", "start", "--tty=false", c.Instance); err != nil {
		return fmt.Errorf("start lima instance: %w", err)
	}
	return nil
}

// Stop shuts the instance down gracefully.
func (c *Client) Stop(ctx context.Context) error {
	if err := c.r.Run(ctx, "limactl", "stop", c.Instance); err != nil {
		return fmt.Errorf("stop lima instance: %w", err)
	}
	return nil
}

// Resize changes the size of an existing instance; zero fields stay as they are.
type Resize struct {
	CPUs      int
	MemoryGiB int
	DiskGiB   int
}

// Edit applies r to the stopped instance's lima.yaml; it takes effect on the next start.
// Lima grows the disk image on start, the guest's growpart extends the root filesystem.
func (c *Client) Edit(ctx context.Context, r Resize) error {
	args := []string{"edit", "--tty=false"}
	for _, f := range []struct {
		flag  string
		value int
	}{{"--cpus", r.CPUs}, {"--memory", r.MemoryGiB}, {"--disk", r.DiskGiB}} {
		if f.value != 0 {
			args = append(args, f.flag, strconv.Itoa(f.value))
		}
	}
	if len(args) == 2 {
		return nil
	}
	if err := c.r.Run(ctx, "limactl", append(args, c.Instance)...); err != nil {
		return fmt.Errorf("resize lima instance: %w", err)
	}
	return nil
}

// Shell runs a command inside the instance with the terminal attached.
func (c *Client) Shell(ctx context.Context, args ...string) error {
	return c.r.Run(ctx, "limactl", c.shellArgs(args)...)
}

// ShellOutput runs a command inside the instance and returns its stdout.
func (c *Client) ShellOutput(ctx context.Context, args ...string) ([]byte, error) {
	return c.r.Output(ctx, "limactl", c.shellArgs(args)...)
}

// WriteFile streams content into a root-owned file inside the instance,
// created with a restrictive umask and renamed into place atomically.
func (c *Client) WriteFile(ctx context.Context, content io.Reader, guestPath, mode string) error {
	if !path.IsAbs(guestPath) {
		return fmt.Errorf("guest path must be absolute: %q", guestPath)
	}
	if !modeRe.MatchString(mode) {
		return fmt.Errorf("invalid file mode %q", mode)
	}
	args := c.shellArgs([]string{"sudo", "sh", "-c", writeScript, "sh", guestPath, mode})
	if err := c.r.RunWithInput(ctx, content, "limactl", args...); err != nil {
		return fmt.Errorf("write %s into lima instance: %w", guestPath, err)
	}
	return nil
}

func (c *Client) shellArgs(args []string) []string {
	return append([]string{"shell", c.Instance, "--"}, args...)
}
