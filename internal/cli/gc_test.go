package cli

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/app"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// ownedHandler lists one VM made by hand and one owned VM created long ago.
func ownedHandler(t *testing.T) runner.FakeHandler {
	t.Helper()
	return func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), config.AgentPath+" ls") {
			return []byte(`[{"name":"vm1","state":"running","ip":"172.16.1.2","vcpus":2,"mem_mib":1024,"created_at":null},
			 {"name":"paseo-3","state":"running","ip":"172.16.3.2","vcpus":2,"mem_mib":3072,
			  "owner":"paseo","slot":"3","created_at":"2026-01-01T00:00:00Z"}]`), nil
		}
		return fakeHandler(t)(name, args)
	}
}

func TestGCRemovesOwnedVMs(t *testing.T) {
	stdout, _, err := runSplit(t, ownedHandler(t), "gc", "--owner", "paseo", "--older-than", "7d")
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if !strings.Contains(stdout, "removed paseo-3 (owner paseo, slot 3, ") {
		t.Errorf("unexpected output:\n%s", stdout)
	}
}

func TestGCDryRunJSON(t *testing.T) {
	fake := runner.NewFake(ownedHandler(t))
	stdout, stderr, err := runSplitWith(t, fake, "gc", "--owner", "paseo", "--keep", "vm2", "--dry-run", "--json")
	if err != nil {
		t.Fatalf("gc --json: %v", err)
	}
	var res app.GCResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, stdout)
	}
	if !res.DryRun || len(res.VMs) != 1 || res.VMs[0].Name != "paseo-3" || res.VMs[0].Slot != "3" || res.HostState == nil {
		t.Errorf("unexpected result: %+v", res)
	}
	if !strings.Contains(stderr, "would remove paseo-3") {
		t.Errorf("progress should go to stderr, got %q", stderr)
	}
	if fake.CalledWithSuffix(config.AgentPath + " rm paseo-3") {
		t.Error("a dry run must not remove the VM")
	}
}

func TestGCKeepSkipsVMs(t *testing.T) {
	fake := runner.NewFake(ownedHandler(t))
	stdout, _, err := runSplitWith(t, fake, "gc", "--owner", "paseo", "--keep", "paseo-3")
	if err != nil {
		t.Fatalf("gc: %v", err)
	}
	if fake.CalledWithSuffix(config.AgentPath+" rm paseo-3") || !strings.Contains(stdout, "nothing to collect") {
		t.Errorf("kept VM was touched or output is off:\n%s\n%s", stdout, fake.Dump())
	}
}

func TestGCRejectsBadAge(t *testing.T) {
	for _, age := range []string{"soon", "-1h", "1.5d", "d"} {
		if _, _, err := runSplit(t, ownedHandler(t), "gc", "--older-than", age); err == nil {
			t.Errorf("--older-than %q should be rejected", age)
		}
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90m": 90 * time.Minute,
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"0":   0,
	} {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("parseAge(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
}

func TestNewPassesOwnerAndSlot(t *testing.T) {
	_, fake, err := run(t, "new", "paseo-3", "--owner", "paseo", "--slot", "3")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	stamped := regexp.MustCompile(` new paseo-3 2 1024 2G snapshot base paseo 3 \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`)
	if !stamped.MatchString(fake.Dump()) {
		t.Errorf("owner and slot not forwarded:\n%s", fake.Dump())
	}
	if _, _, err := run(t, "new", "box", "--slot", "3"); err == nil {
		t.Error("--slot without --owner must be rejected")
	}
}

func TestLsAndInspectShowMetadata(t *testing.T) {
	stdout, _, err := runSplit(t, ownedHandler(t), "ls")
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "OWNER") || !strings.Contains(lines[0], "AGE") {
		t.Fatalf("unexpected table:\n%s", stdout)
	}
	if !strings.Contains(lines[2], "paseo/3") || !strings.HasSuffix(strings.Fields(lines[2])[8], "d") {
		t.Errorf("owned VM row lacks owner/slot or age: %q", lines[2])
	}
	stdout, _, err = runSplit(t, ownedHandler(t), "inspect", "paseo-3")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, want := range []string{"Owner:", "paseo", "Slot:", "Created:", "2026-01-01"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect output lacks %q:\n%s", want, stdout)
		}
	}
}
