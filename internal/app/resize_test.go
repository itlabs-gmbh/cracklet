package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/agent"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/lima"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// sizedHandler reports a Lima instance of the given size and answers the
// agent's ls with vms (a JSON array).
func sizedHandler(status string, cpus, memGiB, diskGiB int, vms string) runner.FakeHandler {
	base := hostHandler(status, agent.Checksum())
	return func(name string, args []string) ([]byte, error) {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "limactl list"):
			return []byte(fmt.Sprintf(`{"name":"cracklet","status":%q,"cpus":%d,"memory":%d,"disk":%d}`+"\n",
				status, cpus, int64(memGiB)<<30, int64(diskGiB)<<30)), nil
		case strings.HasSuffix(joined, config.AgentPath+" ls"):
			return []byte(vms), nil
		}
		return base(name, args)
	}
}

func explicitSize(cpus, memGiB, diskGiB int) PrepareOptions {
	return PrepareOptions{CPUs: cpus, MemoryGiB: memGiB, DiskGiB: diskGiB,
		Explicit: SizeFlags{CPUs: true, Memory: true, Disk: true}}
}

// callIndex returns the position of the first call equal to line, or -1.
func callIndex(fake *runner.Fake, line string) int {
	for i, c := range fake.Calls() {
		if c == line {
			return i
		}
	}
	return -1
}

func TestPrepareGrowsRunningInstance(t *testing.T) {
	app, fake, out, paths := prepareApp(t, sizedHandler("Running", 4, 8, 40, `[{"name":"vm1","state":"stopped"}]`), true)
	writeDummyKeys(t, paths)
	if err := app.Prepare(context.Background(), explicitSize(8, 16, 40)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stop := callIndex(fake, "limactl stop cracklet")
	edit := callIndex(fake, "limactl edit --tty=false --cpus 8 --memory 16 cracklet")
	start := callIndex(fake, "limactl start --tty=false cracklet")
	if stop < 0 || edit < 0 || start < 0 || !(stop < edit && edit < start) {
		t.Fatalf("expected stop, edit (unchanged disk left out), start in order:\n%s", fake.Dump())
	}
	if !fake.CalledWithSuffix(config.RootfsSHA256 + " 2 1024") {
		t.Errorf("prepare must carry on after the resize:\n%s", fake.Dump())
	}
	if !strings.Contains(out.String(), "4 → 8 CPUs") || !strings.Contains(out.String(), "8 → 16 GiB RAM") {
		t.Errorf("output should describe the change, got:\n%s", out.String())
	}
}

func TestPrepareResizesStoppedInstanceWithoutCheckingVMs(t *testing.T) {
	app, fake, _, paths := prepareApp(t, sizedHandler("Stopped", 4, 8, 40, ""), true)
	writeDummyKeys(t, paths)
	if err := app.Prepare(context.Background(), explicitSize(4, 8, 80)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if fake.Called("limactl stop cracklet") || fake.CalledWithSuffix(config.AgentPath+" ls") {
		t.Errorf("a stopped instance needs neither stop nor a microVM check:\n%s", fake.Dump())
	}
	edit := callIndex(fake, "limactl edit --tty=false --disk 80 cracklet")
	start := callIndex(fake, "limactl start --tty=false cracklet")
	if edit < 0 || start < 0 || edit > start {
		t.Errorf("expected edit, then start:\n%s", fake.Dump())
	}
}

func TestPrepareRefusesResizeWhileMicroVMsRun(t *testing.T) {
	vms := `[{"name":"vm1","state":"running"},{"name":"vm2","state":"stopped"},{"name":"dev","state":"running"}]`
	app, fake, _, paths := prepareApp(t, sizedHandler("Running", 4, 8, 40, vms), true)
	writeDummyKeys(t, paths)
	err := app.Prepare(context.Background(), explicitSize(8, 8, 40))
	if err == nil || !strings.Contains(err.Error(), "vm1, dev") || !strings.Contains(err.Error(), "cracklet stop") {
		t.Fatalf("expected a refusal naming the running microVMs, got %v", err)
	}
	assertUntouched(t, fake)
}

// assertUntouched fails if the instance was stopped or edited.
func assertUntouched(t *testing.T, fake *runner.Fake) {
	t.Helper()
	for _, c := range fake.Calls() {
		if strings.HasPrefix(c, "limactl stop") || strings.HasPrefix(c, "limactl edit") {
			t.Errorf("the instance must not be touched: %s", c)
		}
	}
}

func TestPrepareRefusesToShrinkDisk(t *testing.T) {
	app, fake, _, paths := prepareApp(t, sizedHandler("Running", 4, 8, 40, "[]"), true)
	writeDummyKeys(t, paths)
	err := app.Prepare(context.Background(), explicitSize(8, 8, 20))
	if err == nil || !strings.Contains(err.Error(), "cannot shrink") {
		t.Fatalf("expected a disk shrink error, got %v", err)
	}
	assertUntouched(t, fake)
}

func TestPrepareIgnoresDefaultSizingOnExistingInstance(t *testing.T) {
	// The VM was grown earlier; a plain re-run carries the defaults but must not shrink it.
	app, fake, _, paths := prepareApp(t, sizedHandler("Running", 8, 16, 80, "[]"), true)
	writeDummyKeys(t, paths)
	o := PrepareOptions{CPUs: config.DefaultLimaCPUs, MemoryGiB: config.DefaultLimaMemoryGiB, DiskGiB: config.DefaultLimaDiskGiB}
	if err := app.Prepare(context.Background(), o); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if fake.Called("limactl stop cracklet") || fake.CalledWithSuffix(config.AgentPath+" ls") {
		t.Errorf("defaults must not resize an existing instance:\n%s", fake.Dump())
	}
}

func TestPrepareSkipsResizeWhenSizeMatches(t *testing.T) {
	app, fake, out, paths := prepareApp(t, sizedHandler("Running", 8, 16, 80, "[]"), true)
	writeDummyKeys(t, paths)
	if err := app.Prepare(context.Background(), explicitSize(8, 16, 80)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if fake.Called("limactl stop cracklet") {
		t.Errorf("re-running with the current size must not restart the instance:\n%s", fake.Dump())
	}
	if !strings.Contains(out.String(), "is running") {
		t.Errorf("expected the usual running message, got:\n%s", out.String())
	}
}

func TestPrepareSurfacesMicroVMCheckFailure(t *testing.T) {
	app, fake, _, paths := prepareApp(t, sizedHandler("Running", 4, 8, 40, "not json"), true)
	writeDummyKeys(t, paths)
	err := app.Prepare(context.Background(), explicitSize(8, 8, 40))
	if err == nil || !strings.Contains(err.Error(), "running microVMs") {
		t.Fatalf("an unreadable microVM list must abort the resize, got %v", err)
	}
	assertUntouched(t, fake)
}

func TestResizeFor(t *testing.T) {
	inst := lima.Instance{Status: lima.StatusRunning, CPUs: 4, Memory: 8 << 30, Disk: 40 << 30}
	all := SizeFlags{CPUs: true, Memory: true, Disk: true}
	for _, tc := range []struct {
		name    string
		inst    lima.Instance
		o       PrepareOptions
		want    lima.Resize
		wantErr string
	}{
		{"nothing explicit", inst, PrepareOptions{CPUs: 1, MemoryGiB: 1, DiskGiB: 10}, lima.Resize{}, ""},
		{"cpus only", inst, PrepareOptions{CPUs: 6, MemoryGiB: 1, Explicit: SizeFlags{CPUs: true}}, lima.Resize{CPUs: 6}, ""},
		{"memory only", inst, PrepareOptions{MemoryGiB: 12, Explicit: SizeFlags{Memory: true}}, lima.Resize{MemoryGiB: 12}, ""},
		{"disk only", inst, PrepareOptions{DiskGiB: 60, Explicit: SizeFlags{Disk: true}}, lima.Resize{DiskGiB: 60}, ""},
		{"shrink cpus and memory", inst, PrepareOptions{CPUs: 2, MemoryGiB: 4, DiskGiB: 40, Explicit: all}, lima.Resize{CPUs: 2, MemoryGiB: 4}, ""},
		{"equal", inst, PrepareOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40, Explicit: all}, lima.Resize{}, ""},
		{"disk shrink", inst, PrepareOptions{CPUs: 8, MemoryGiB: 8, DiskGiB: 39, Explicit: all}, lima.Resize{}, "cannot shrink"},
		{"unknown size", lima.Instance{Status: lima.StatusStopped}, PrepareOptions{CPUs: 8, Explicit: SizeFlags{CPUs: true}}, lima.Resize{}, "no size"},
		{"broken", lima.Instance{Status: "Broken"}, PrepareOptions{CPUs: 8, Explicit: SizeFlags{CPUs: true}}, lima.Resize{}, "Broken"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.o.resizeFor(tc.inst)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("resizeFor = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestPrepareResizeFailuresExplainTheVMState(t *testing.T) {
	for _, tc := range []struct {
		failing, wantMsg string
		startAfter       bool
	}{
		{"limactl stop", "", false},
		{"limactl edit", "same size flags", false},
		{"limactl start", "new size is saved", true},
	} {
		t.Run(tc.failing, func(t *testing.T) {
			base := sizedHandler("Running", 4, 8, 40, "[]")
			app, fake, _, paths := prepareApp(t, func(name string, args []string) ([]byte, error) {
				if strings.HasPrefix(name+" "+strings.Join(args, " "), tc.failing) {
					return nil, errors.New("boom")
				}
				return base(name, args)
			}, true)
			writeDummyKeys(t, paths)
			err := app.Prepare(context.Background(), explicitSize(8, 8, 40))
			if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("want wrapped error mentioning %q, got %v", tc.wantMsg, err)
			}
			if got := fake.Called("limactl start --tty=false cracklet"); got != tc.startAfter {
				t.Errorf("start called = %v, want %v:\n%s", got, tc.startAfter, fake.Dump())
			}
			if fake.CalledWithSuffix(config.RootfsSHA256 + " 2 1024") {
				t.Errorf("prepare must stop after a failed resize:\n%s", fake.Dump())
			}
		})
	}
}
