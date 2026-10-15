package lima

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/agent"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// agentHandler answers the agent probe with checksum (or fails it) and every
// agent `ls` with an empty list; it counts the probes.
func agentHandler(checksum string, failProbe *atomic.Bool, probes *atomic.Int32) runner.FakeHandler {
	return func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "sha256sum"):
			probes.Add(1)
			if failProbe != nil && failProbe.Load() {
				return nil, errors.New("limactl shell: connection refused")
			}
			return []byte(checksum + "\n"), nil
		case strings.HasSuffix(joined, config.AgentPath+" ls"):
			return []byte("[]"), nil
		case strings.Contains(joined, "mkdir -p"): // WriteFile of the agent script
			return nil, nil
		}
		return nil, errors.New("unexpected command: " + joined)
	}
}

func agentBackend(t *testing.T, h runner.FakeHandler) (*Backend, *runner.Fake) {
	t.Helper()
	fake := runner.NewFake(h)
	out := &bytes.Buffer{}
	return New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, out, out), fake
}

func TestAgentIsProbedOncePerProcess(t *testing.T) {
	var probes atomic.Int32
	b, fake := agentBackend(t, agentHandler(agent.Checksum(), nil, &probes))
	for i := 0; i < 3; i++ {
		if _, err := b.List(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if probes.Load() != 1 {
		t.Errorf("want one probe for three listings, got %d:\n%s", probes.Load(), fake.Dump())
	}
	if fake.CalledWithSuffix(config.AgentPath + " 0755") {
		t.Errorf("a current agent must not be reinstalled:\n%s", fake.Dump())
	}
}

func TestOutdatedAgentIsInstalledOnce(t *testing.T) {
	var probes atomic.Int32
	b, fake := agentBackend(t, agentHandler("stale", nil, &probes))
	for i := 0; i < 2; i++ {
		if _, err := b.List(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	installs := 0
	for _, c := range fake.Calls() {
		if strings.Contains(c, "mkdir -p") && strings.Contains(c, config.AgentPath) {
			installs++
		}
	}
	if probes.Load() != 1 || installs != 1 {
		t.Errorf("want one probe and one install, got %d and %d:\n%s", probes.Load(), installs, fake.Dump())
	}
	if in, ok := fake.Input(config.AgentPath + " 0755"); !ok || in != agent.Script {
		t.Errorf("the embedded agent script must be streamed to the Lima VM")
	}
}

func TestFailedProbeIsRetried(t *testing.T) {
	var probes atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	b, _ := agentBackend(t, agentHandler(agent.Checksum(), &fail, &probes))
	if _, err := b.List(context.Background()); err == nil || !strings.Contains(err.Error(), "probe guest agent") {
		t.Fatalf("a failed probe must surface, got %v", err)
	}
	fail.Store(false)
	if _, err := b.List(context.Background()); err != nil {
		t.Fatalf("listing after the probe recovered: %v", err)
	}
	if probes.Load() != 2 {
		t.Errorf("a failed probe must not be cached, got %d probes", probes.Load())
	}
}

func TestConcurrentCallersProbeOnce(t *testing.T) {
	var probes atomic.Int32
	b, _ := agentBackend(t, agentHandler(agent.Checksum(), nil, &probes))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.List(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if probes.Load() != 1 {
		t.Errorf("want one probe under concurrency, got %d", probes.Load())
	}
}
