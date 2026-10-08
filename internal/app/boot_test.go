package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/agent"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// limaAfterReboot simulates a Lima VM that is stopped until `limactl start`
// runs (or, with startedElsewhere, until it is listed once more), with a
// current agent whose restore reports restore.
func limaAfterReboot(restore string, startedElsewhere bool) runner.FakeHandler {
	var mu sync.Mutex
	status, lists := "Stopped", 0
	return func(name string, args []string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "limactl list"):
			lists++
			if startedElsewhere && lists > 1 {
				status = "Running"
			}
			return []byte(`{"name":"cracklet","status":"` + status + `"}` + "\n"), nil
		case joined == "limactl start --tty=false cracklet":
			status = "Running"
			return nil, nil
		case strings.Contains(joined, "sha256sum"):
			return []byte(agent.Checksum() + "\n"), nil
		case strings.HasSuffix(joined, config.AgentPath+" restore"):
			if status != "Running" {
				return nil, errors.New("restore before the Lima VM runs")
			}
			return []byte(restore), nil
		case strings.HasSuffix(joined, config.AgentPath+" ls"):
			return []byte(`[]`), nil
		}
		return nil, errors.New("unexpected command: " + joined)
	}
}

func TestStoppedLimaIsStartedAndMicroVMsRestored(t *testing.T) {
	app, fake, out := newTestApp(t, limaAfterReboot(`{"started":["paseo-1","paseo-2"],"failed":["paseo-3"]}`, false))
	if _, err := app.ListVMs(context.Background()); err != nil {
		t.Fatalf("ListVMs after a reboot: %v", err)
	}
	calls := strings.Join(fake.Calls(), "\n")
	start := strings.Index(calls, "limactl start --tty=false cracklet")
	restore := strings.Index(calls, config.AgentPath+" restore")
	list := strings.Index(calls, config.AgentPath+" ls")
	if start < 0 || restore < start || list < restore {
		t.Fatalf("want start, restore, ls in that order, got:\n%s", calls)
	}
	for _, want := range []string{
		`Starting Lima VM "cracklet"`,
		"Restarted microVMs: paseo-1, paseo-2",
		"paseo-3 did not come back",
		"cracklet start paseo-3",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestBootWithNothingToRestoreStaysQuiet(t *testing.T) {
	app, _, out := newTestApp(t, limaAfterReboot(`{"started":[],"failed":[]}`, false))
	if _, err := app.ListVMs(context.Background()); err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if strings.Contains(out.String(), "microVM") {
		t.Errorf("nothing was restored, so nothing should be reported:\n%s", out.String())
	}
}

// TestLimaStartedByAnotherProcessIsNotStartedAgain: a command that waited for
// another one's boot must not run `limactl start` a second time, but still
// waits for the restore so it sees the microVMs running.
func TestLimaStartedByAnotherProcessIsNotStartedAgain(t *testing.T) {
	app, fake, _ := newTestApp(t, limaAfterReboot(`{"started":[],"failed":[]}`, true))
	if _, err := app.ListVMs(context.Background()); err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if fake.Called("limactl start --tty=false cracklet") {
		t.Errorf("the Lima VM was already running again:\n%s", fake.Dump())
	}
	if !fake.CalledWithSuffix(config.AgentPath + " restore") {
		t.Errorf("restore must still be awaited:\n%s", fake.Dump())
	}
}

// TestRunningLimaWaitsForAnOngoingRestore: a command that finds the Lima VM
// already running while another one still restores its microVMs waits for
// that restore, so `exec` or `ls` do not see the VMs half back.
func TestRunningLimaWaitsForAnOngoingRestore(t *testing.T) {
	restoring, finish := make(chan struct{}), make(chan struct{})
	base := limaAfterReboot(`{"started":["vm1"],"failed":[]}`, false)
	app, _, _ := newTestApp(t, func(name string, args []string) ([]byte, error) {
		if strings.HasSuffix(strings.Join(args, " "), config.AgentPath+" restore") {
			close(restoring)
			<-finish
		}
		return base(name, args)
	})
	booted := make(chan error, 1)
	go func() { booted <- app.requireRunning(context.Background()) }()
	<-restoring // the first command has started Lima and is restoring
	second := make(chan error, 1)
	go func() { second <- app.requireRunning(context.Background()) }()
	select {
	case err := <-second:
		t.Fatalf("the second command returned before the restore finished: %v", err)
	case <-time.After(3 * bootLockPoll):
	}
	close(finish)
	for _, ch := range []chan error{booted, second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("command did not finish after the restore")
		}
	}
}

func TestStoppedLimaIsNotStartedDuringResize(t *testing.T) {
	app, fake, _ := newTestApp(t, limaAfterReboot(`{"started":[],"failed":[]}`, false))
	release, err := app.holdLimaForResize()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := app.ListVMs(context.Background()); !errors.Is(err, errResizing) {
		t.Fatalf("a resize stops the Lima VM on purpose, got %v", err)
	}
	if fake.Called("limactl start --tty=false cracklet") {
		t.Errorf("the Lima VM must not be started under a resize:\n%s", fake.Dump())
	}
}

func TestLimaStartFailureSurfaces(t *testing.T) {
	app, fake, _ := newTestApp(t, failingAt(limaAfterReboot(`{"started":[],"failed":[]}`, false), "limactl start --tty=false cracklet"))
	if _, err := app.ListVMs(context.Background()); err == nil || !strings.Contains(err.Error(), "start lima instance") {
		t.Fatalf("expected the start failure, got %v", err)
	}
	if fake.CalledWithSuffix(config.AgentPath + " restore") {
		t.Errorf("no restore without a running Lima VM:\n%s", fake.Dump())
	}
}

// TestRestoreFailureWarnsButCommandRuns: once the Lima VM is up, the command
// goes on; the restore failure is reported, not fatal.
func TestRestoreFailureWarnsButCommandRuns(t *testing.T) {
	app, _, out := newTestApp(t, failingAt(limaAfterReboot("", false), config.AgentPath+" restore"))
	if _, err := app.ListVMs(context.Background()); err != nil {
		t.Fatalf("ListVMs must still work with the Lima VM up: %v", err)
	}
	if !strings.Contains(out.String(), "restarting its microVMs failed") || !strings.Contains(out.String(), "injected failure") {
		t.Errorf("the restore failure must be reported:\n%s", out.String())
	}
}

// TestBootNotesStayOffStdout: the notes go to the status writer, so the
// first `cracklet exec ... | cmd` after a reboot gets clean output.
func TestBootNotesStayOffStdout(t *testing.T) {
	fake := runner.NewFake(limaAfterReboot(`{"started":["vm1"],"failed":[]}`, false))
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	paths := config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}
	app := New(fake, paths, stdout, WithEnvd(fakeEnvd), WithStatus(stderr))
	if err := app.requireRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Restarted microVMs: vm1") {
		t.Errorf("stdout %q, status %q", stdout.String(), stderr.String())
	}
}

// TestPollingNeverStartsLima: the tunnel's background check must not undo a
// deliberate 'limactl stop'.
func TestPollingNeverStartsLima(t *testing.T) {
	app, fake, _ := newTestApp(t, limaAfterReboot(`{"started":[],"failed":[]}`, false))
	if _, err := app.vmRunning(context.Background(), "vm1"); err == nil || !strings.Contains(err.Error(), "is stopped") {
		t.Fatalf("expected a stopped Lima VM to be reported, got %v", err)
	}
	if fake.Called("limactl start --tty=false cracklet") {
		t.Errorf("polling started the Lima VM:\n%s", fake.Dump())
	}
}

func TestBrokenLimaPointsAtPrepare(t *testing.T) {
	app, fake, _ := newTestApp(t, func(string, []string) ([]byte, error) {
		return []byte(`{"name":"cracklet","status":"Broken"}` + "\n"), nil
	})
	if _, err := app.ListVMs(context.Background()); err == nil || !strings.Contains(err.Error(), "is Broken") {
		t.Fatalf("expected a Broken hint, got %v", err)
	}
	if fake.Called("limactl start --tty=false cracklet") {
		t.Errorf("only a stopped Lima VM is started:\n%s", fake.Dump())
	}
}

func TestLimaBootLockWaitsAndHonoursCancel(t *testing.T) {
	a, _, _ := newTestApp(t, defaultHandler(nil))
	release, err := a.waitForLimaBoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.waitForLimaBoot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a held boot lock must block until ctx ends, got %v", err)
	}
	done := make(chan error, 1)
	go func() {
		r, err := a.waitForLimaBoot(context.Background())
		if err == nil {
			r()
		}
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waiter after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not get the lock after release")
	}
}
