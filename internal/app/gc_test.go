package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/grant"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

var gcNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// gcList has one VM of every kind gc has to tell apart.
const gcList = `[
 {"name":"manual","state":"running","owner":null,"slot":null,"created_at":"2026-10-01T12:00:00Z"},
 {"name":"paseo-1","state":"running","owner":"paseo","slot":"1","created_at":"2026-10-07T09:00:00Z"},
 {"name":"paseo-2","state":"running","owner":"paseo","slot":"2","created_at":"2026-10-07T11:50:00Z"},
 {"name":"ci-7","state":"stopped","owner":"ci","slot":null,"created_at":"2026-10-07T07:00:00Z"},
 {"name":"undated","state":"broken","owner":"paseo","slot":null,"created_at":null},
 {"name":"paseo-9","state":"creating","owner":"paseo","slot":"9","created_at":"2026-10-07T08:00:00Z"},
 {"name":"paseo-4","state":"stopped","owner":"paseo","slot":"4","created_at":"2026-10-07T08:00:00Z"}
]`

// gcHandler serves gcList and records which VMs the agent removed; failRm
// makes the removal of that VM fail. paseo-4 was removed by someone else
// after the listing, so the agent reports it gone. Removed VMs vanish from
// later listings.
func gcHandler(removed *[]string, failRm string) runner.FakeHandler {
	return gcHandlerReborn(removed, failRm, "")
}

// gcHandlerReborn is gcHandler where reborn is created again under the same
// name right after gc removed it, as a concurrent `new --grant` would.
func gcHandlerReborn(removed *[]string, failRm, reborn string) runner.FakeHandler {
	vanished := map[string]bool{}
	return func(name string, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if _, rest, ok := strings.Cut(joined, config.AgentPath+" rm "); ok {
			vmName := strings.Fields(rest)[0]
			switch vmName {
			case failRm:
				return nil, errors.New("agent rm: VM is wedged")
			case "paseo-4":
				vanished[vmName] = true
				return []byte("gone\n"), nil
			}
			*removed = append(*removed, rest)
			vanished[vmName] = vmName != reborn
			return []byte("removed\n"), nil
		}
		if strings.HasSuffix(joined, config.AgentPath+" ls") {
			return listWithout(vanished), nil
		}
		return defaultHandler(nil)(name, args)
	}
}

func listWithout(vanished map[string]bool) []byte {
	var vms []map[string]any
	if err := json.Unmarshal([]byte(gcList), &vms); err != nil {
		panic(err)
	}
	kept := []map[string]any{}
	for _, v := range vms {
		if !vanished[v["name"].(string)] {
			kept = append(kept, v)
		}
	}
	out, _ := json.Marshal(kept)
	return out
}

func firstWords(calls []string) string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, strings.Fields(c)[0])
	}
	return strings.Join(out, ",")
}

func TestGCRemovesOnlyTheListedVM(t *testing.T) {
	var removed []string
	a, _ := newGCApp(t, gcHandler(&removed, ""))
	if _, err := a.GC(context.Background(), GCOptions{Owner: "paseo"}); err != nil {
		t.Fatalf("GC: %v", err)
	}
	// owner and creation time let the agent refuse a VM recreated under the same name
	want := "paseo-1 paseo 2026-10-07T09:00:00Z,paseo-2 paseo 2026-10-07T11:50:00Z,undated paseo -"
	if got := strings.Join(removed, ","); got != want {
		t.Errorf("rm calls %q, want %q", got, want)
	}
}

func newGCApp(t *testing.T, handle runner.FakeHandler) (*App, *runner.Fake) {
	t.Helper()
	a, fake, _ := newTestApp(t, handle)
	a.now = func() time.Time { return gcNow }
	return a, fake
}

func names(vms []VMInfo) string {
	out := make([]string, 0, len(vms))
	for _, v := range vms {
		out = append(out, v.Name)
	}
	return strings.Join(out, ",")
}

func TestGCSelectsOwnedVMs(t *testing.T) {
	cases := []struct {
		opts GCOptions
		want string
	}{
		// paseo-9 is still being created, paseo-4 vanished meanwhile
		{GCOptions{Owner: "paseo"}, "paseo-1,paseo-2,undated"},
		{GCOptions{Owner: "paseo", Keep: []string{"paseo-2", "undated"}}, "paseo-1"},
		// unknown age is never old, and VMs made by hand are never collected
		{GCOptions{OlderThan: time.Hour}, "paseo-1,ci-7"},
		{GCOptions{Owner: "ci", OlderThan: 6 * time.Hour}, ""},
		{GCOptions{Owner: "nobody"}, ""},
	}
	for _, c := range cases {
		var removed []string
		a, _ := newGCApp(t, gcHandler(&removed, ""))
		res, err := a.GC(context.Background(), c.opts)
		if err != nil {
			t.Fatalf("GC(%+v): %v", c.opts, err)
		}
		if got := names(res.VMs); got != c.want {
			t.Errorf("GC(%+v) reported %q, want %q", c.opts, got, c.want)
		}
		if got := firstWords(removed); got != c.want {
			t.Errorf("GC(%+v) removed %q, want %q", c.opts, got, c.want)
		}
	}
}

func TestGCDryRunRemovesNothing(t *testing.T) {
	var removed []string
	a, _ := newGCApp(t, gcHandler(&removed, ""))
	store := grant.Store{Dir: a.paths.VMsDir()}
	if err := store.Save("ghost", grant.Set{{Cap: "claude"}}); err != nil {
		t.Fatal(err)
	}
	res, err := a.GC(context.Background(), GCOptions{Owner: "paseo", DryRun: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if !res.DryRun || names(res.VMs) != "paseo-1,paseo-2,undated,paseo-4" || strings.Join(res.HostState, ",") != "ghost" {
		t.Errorf("unexpected result: %+v", res)
	}
	if len(removed) != 0 {
		t.Errorf("a dry run must not remove VMs, removed %v", removed)
	}
	if set, _ := store.Load("ghost"); len(set) != 1 {
		t.Error("a dry run must not remove host state")
	}
}

func TestGCPrunesHostStateOfVanishedVMs(t *testing.T) {
	var removed []string
	a, fake := newGCApp(t, gcHandler(&removed, ""))
	store := grant.Store{Dir: a.paths.VMsDir()}
	for _, name := range []string{"ghost", "manual", "paseo-1"} {
		if err := store.Save(name, grant.Set{{Cap: "claude"}}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := a.GC(context.Background(), GCOptions{})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(res.VMs) != 0 || len(removed) != 0 {
		t.Errorf("without --owner or --older-than gc removes no VM, got %+v / %v", res.VMs, removed)
	}
	if strings.Join(res.HostState, ",") != "ghost" {
		t.Errorf("pruned %v, want [ghost]", res.HostState)
	}
	for name, want := range map[string]int{"ghost": 0, "manual": 1, "paseo-1": 1} {
		if set, _ := store.Load(name); len(set) != want {
			t.Errorf("%s has %d grants, want %d", name, len(set), want)
		}
	}
	// the second listing is the liveness check under the VMs' locks
	lists := 0
	for _, c := range fake.Calls() {
		if strings.HasSuffix(c, config.AgentPath+" ls") {
			lists++
		}
	}
	if lists != 2 {
		t.Errorf("expected a re-check before pruning, got %d listings:\n%s", lists, fake.Dump())
	}
}

func TestGCRemovesStateOfCollectedVMs(t *testing.T) {
	var removed []string
	a, _ := newGCApp(t, gcHandler(&removed, ""))
	store := grant.Store{Dir: a.paths.VMsDir()}
	if err := store.Save("paseo-1", grant.Set{{Cap: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GC(context.Background(), GCOptions{Owner: "paseo", Keep: []string{"paseo-2", "undated"}}); err != nil {
		t.Fatalf("GC: %v", err)
	}
	if set, _ := store.Load("paseo-1"); len(set) != 0 {
		t.Errorf("grants of a collected VM must go with it, got %v", set)
	}
}

func TestGCKeepsStateOfAVMRecreatedMeanwhile(t *testing.T) {
	var removed []string
	a, _ := newGCApp(t, gcHandlerReborn(&removed, "", "paseo-1"))
	store := grant.Store{Dir: a.paths.VMsDir()}
	if err := store.Save("paseo-1", grant.Set{{Cap: "claude"}}); err != nil {
		t.Fatal(err)
	}
	res, err := a.GC(context.Background(), GCOptions{Owner: "paseo", Keep: []string{"paseo-2", "undated"}})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if names(res.VMs) != "paseo-1" {
		t.Fatalf("paseo-1 should have been collected, got %q", names(res.VMs))
	}
	// the grants now belong to the new paseo-1 and must survive
	if set, _ := store.Load("paseo-1"); len(set) != 1 {
		t.Errorf("grants of the recreated VM were deleted")
	}
	if len(res.HostState) != 0 {
		t.Errorf("no leftovers expected, got %v", res.HostState)
	}
}

func TestGCPrunesStateOfAVMGoneMeanwhile(t *testing.T) {
	var removed []string
	a, _ := newGCApp(t, gcHandler(&removed, ""))
	store := grant.Store{Dir: a.paths.VMsDir()}
	if err := store.Save("paseo-4", grant.Set{{Cap: "claude"}}); err != nil {
		t.Fatal(err)
	}
	res, err := a.GC(context.Background(), GCOptions{Owner: "paseo"})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if set, _ := store.Load("paseo-4"); len(set) != 0 {
		t.Errorf("leftover grants of a VM removed by someone else must go in the same run")
	}
	if strings.Join(res.HostState, ",") != "paseo-4" || strings.Contains(names(res.VMs), "paseo-4") {
		t.Errorf("paseo-4 is a host-state leftover, not a collected VM: %+v", res)
	}
}

func TestGCContinuesAfterAFailedRemoval(t *testing.T) {
	var removed []string
	a, _ := newGCApp(t, gcHandler(&removed, "paseo-1"))
	res, err := a.GC(context.Background(), GCOptions{Owner: "paseo"})
	if err == nil || !strings.Contains(err.Error(), "paseo-1") {
		t.Fatalf("expected an error naming paseo-1, got %v", err)
	}
	if firstWords(removed) != "paseo-2,undated" || names(res.VMs) != "paseo-2,undated" {
		t.Errorf("the other VMs must still be collected: removed %v, reported %q", removed, names(res.VMs))
	}
}

func TestGCRejectsInvalidOptions(t *testing.T) {
	for _, opts := range []GCOptions{
		{Owner: "a b"},
		{Owner: "-"},
		{OlderThan: -time.Hour},
		{Owner: "paseo", Keep: []string{"../etc"}},
	} {
		a, fake := newGCApp(t, gcHandler(new([]string), ""))
		if _, err := a.GC(context.Background(), opts); err == nil {
			t.Errorf("GC(%+v) expected an error", opts)
		}
		if len(fake.Calls()) != 0 {
			t.Errorf("GC(%+v) must not run commands, got:\n%s", opts, fake.Dump())
		}
	}
}

func TestNewPassesOwnerAndSlot(t *testing.T) {
	cases := map[string]vm.Spec{
		// the Mac stamps owned VMs, in UTC, because gc measures ages with its clock
		" new vm1 2 1024 2G snapshot base paseo 3 2026-10-07T12:00:00Z": {Name: "vm1", VCPUs: 2, MemMiB: 1024, Owner: "paseo", Slot: "3"},
		" new vm1 2 1024 2G snapshot base paseo - 2026-10-07T12:00:00Z": {Name: "vm1", VCPUs: 2, MemMiB: 1024, Owner: "paseo"},
	}
	for want, spec := range cases {
		a, fake, _ := newTestApp(t, defaultHandler(map[string]string{
			"new": `{"name":"vm1","state":"running","owner":"paseo","slot":"3","created_at":"2026-10-07T12:00:00Z"}`,
		}))
		a.now = func() time.Time { return gcNow.In(time.FixedZone("CEST", 2*3600)) }
		info, err := a.NewVM(context.Background(), spec)
		if err != nil {
			t.Fatalf("NewVM: %v", err)
		}
		if !fake.CalledWithSuffix(config.AgentPath + want) {
			t.Errorf("expected agent call ending in %q, got:\n%s", want, fake.Dump())
		}
		if info.Owner != "paseo" || info.Slot != "3" || info.CreatedAt == nil || !info.CreatedAt.Equal(gcNow) {
			t.Errorf("metadata not parsed: %+v", info)
		}
	}
}

func TestHumanAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-time.Second:        "0s",
		45 * time.Second:    "45s",
		12 * time.Minute:    "12m",
		47 * time.Hour:      "47h",
		49 * time.Hour:      "2d",
		10 * 24 * time.Hour: "10d",
	} {
		if got := HumanAge(d); got != want {
			t.Errorf("HumanAge(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestVMInfoJSONKeepsMetadataKeys(t *testing.T) {
	data, err := json.Marshal(VMInfo{Name: "vm1"})
	if err != nil {
		t.Fatal(err)
	}
	// consumers see the same keys for every VM, as the agent reports them
	if !strings.Contains(string(data), `"owner":"","slot":"","created_at":null`) {
		t.Errorf("metadata keys missing: %s", data)
	}
}

func TestNewVMPrunesLeftoversBeforeTheVMRuns(t *testing.T) {
	store := grant.Store{}
	var leftoverAtCreate []string
	a, _, _ := newTestApp(t, func(name string, args []string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasSuffix(joined, config.AgentPath+" ls"):
			return []byte(`[{"name":"agent1","state":"running"}]`), nil
		case strings.Contains(joined, config.AgentPath+" new "):
			// the broker reads grants by name as soon as the VM runs
			leftoverAtCreate, _ = store.Names()
			return []byte(`{"name":"vm3","state":"running"}`), nil
		}
		return defaultHandler(nil)(name, args)
	})
	store.Dir = a.paths.VMsDir()
	for _, vmName := range []string{"agent1", "vm3"} { // vm3: an earlier VM the agent will auto-name again
		if err := store.Save(vmName, grant.Set{{Cap: "claude"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.NewVM(context.Background(), vm.Spec{VCPUs: 2, MemMiB: 1024}); err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	if strings.Join(leftoverAtCreate, ",") != "agent1" {
		t.Errorf("state left when the VM started: %v, want only the live agent1", leftoverAtCreate)
	}
	if set, _ := store.Load("agent1"); len(set) != 1 {
		t.Error("grants of a live VM must survive")
	}
}

func TestNewVMStartsWithoutLeftoverGrants(t *testing.T) {
	// paseo-1 still existed when NewVM pruned and was replaced right after
	// (gc removing it); the clear after creation catches that ordering
	a, _, _ := newTestApp(t, defaultHandler(map[string]string{
		"ls":  `[{"name":"paseo-1","state":"running","owner":"paseo"}]`,
		"new": `{"name":"paseo-1","state":"running","owner":"paseo","slot":"1","created_at":"2026-10-07T12:00:00Z"}`,
	}))
	store := grant.Store{Dir: a.paths.VMsDir()}
	// state of an earlier paseo-1 that gc kept because the new one already existed
	if err := store.Save("paseo-1", grant.Set{{Cap: "github", Scope: "org/private"}}); err != nil {
		t.Fatal(err)
	}
	oldToken, err := store.Token("paseo-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.NewVM(context.Background(), vm.Spec{Name: "paseo-1", VCPUs: 2, MemMiB: 1024, Owner: "paseo", Slot: "1"}); err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	if set, _ := store.Load("paseo-1"); len(set) != 0 {
		t.Errorf("a new VM must not inherit the grants of an earlier VM with its name, got %v", set)
	}
	if tok, _ := store.Token("paseo-1"); tok == oldToken {
		t.Error("a new VM must not inherit the placeholder token either")
	}
}

func TestNewVMKeepsAGrantGivenWhileItStarts(t *testing.T) {
	store := grant.Store{}
	granted := make(chan struct{})
	a, _, _ := newTestApp(t, func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), config.AgentPath+" new ") {
			// `cracklet grant box claude` in another terminal, as soon as box runs
			go func() {
				defer close(granted)
				unlock, err := store.Lock("box")
				if err != nil {
					t.Error(err)
					return
				}
				defer unlock()
				if err := store.Save("box", grant.Set{{Cap: "claude"}}); err != nil {
					t.Error(err)
				}
			}()
			select { // give it the chance to land before NewVM clears
			case <-granted:
			case <-time.After(100 * time.Millisecond):
			}
			return []byte(`{"name":"box","state":"running"}`), nil
		}
		return defaultHandler(nil)(name, args)
	})
	store.Dir = a.paths.VMsDir()
	if _, err := a.NewVM(context.Background(), vm.Spec{Name: "box", VCPUs: 2, MemMiB: 1024}); err != nil {
		t.Fatalf("NewVM: %v", err)
	}
	<-granted
	if set, _ := store.Load("box"); len(set) != 1 {
		t.Errorf("a grant given while the VM started must survive, got %v", set)
	}
}

func TestNewVMKeepsGrantsWhenTheNameIsTaken(t *testing.T) {
	a, _, _ := newTestApp(t, func(name string, args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), config.AgentPath+" new ") {
			return nil, errors.New("agent new: VM 'paseo-1' already exists")
		}
		return defaultHandler(nil)(name, args)
	})
	store := grant.Store{Dir: a.paths.VMsDir()}
	if err := store.Save("paseo-1", grant.Set{{Cap: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.NewVM(context.Background(), vm.Spec{Name: "paseo-1", VCPUs: 2, MemMiB: 1024}); err == nil {
		t.Fatal("expected the agent's error")
	}
	if set, _ := store.Load("paseo-1"); len(set) != 1 {
		t.Error("a failed new must not touch the grants of the existing VM")
	}
}
