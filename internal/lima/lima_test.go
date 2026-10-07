package lima

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

func TestGetFindsInstance(t *testing.T) {
	fake := runner.NewFake(func(name string, args []string) ([]byte, error) {
		return []byte(`{"name":"cracklet","status":"Running","dir":"/x"}` + "\n"), nil
	})
	inst, ok, err := NewClient(fake, "cracklet").Get(context.Background())
	if err != nil || !ok || inst.Status != StatusRunning {
		t.Fatalf("Get = %+v, %v, %v", inst, ok, err)
	}
	if !fake.Called("limactl list --format json") {
		t.Errorf("unexpected calls:\n%s", fake.Dump())
	}
}

func TestGetMissingInstance(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return []byte(""), nil })
	_, ok, err := NewClient(fake, "cracklet").Get(context.Background())
	if err != nil || ok {
		t.Fatalf("expected not found, got ok=%v err=%v", ok, err)
	}
}

func TestGetPropagatesErrors(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, errors.New("boom") })
	if _, _, err := NewClient(fake, "cracklet").Get(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestCommandLines(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	c := NewClient(fake, "cracklet")
	ctx := context.Background()
	_ = c.Create(ctx, "/tmp/lima.yaml")
	_ = c.Start(ctx)
	_ = c.Shell(ctx, "sudo", "true")
	_, _ = c.ShellOutput(ctx, "cat", "/etc/hostname")
	_ = c.WriteFile(ctx, strings.NewReader("x"), "/usr/local/lib/cracklet/agent.sh", "0755")

	for _, want := range []string{
		"limactl start --tty=false --name cracklet /tmp/lima.yaml",
		"limactl start --tty=false cracklet",
		"limactl shell cracklet -- sudo true",
		"limactl shell cracklet -- cat /etc/hostname",
		"limactl shell cracklet -- sudo sh -c " + writeScript + " sh /usr/local/lib/cracklet/agent.sh 0755",
	} {
		if !fake.Called(want) {
			t.Errorf("missing call %q in:\n%s", want, fake.Dump())
		}
	}
	if in, ok := fake.Input("sh /usr/local/lib/cracklet/agent.sh 0755"); !ok || in != "x" {
		t.Errorf("content must be streamed to stdin, got %q ok=%v", in, ok)
	}
}

func TestWriteFileValidatesArguments(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	c := NewClient(fake, "cracklet")
	ctx := context.Background()
	if err := c.WriteFile(ctx, strings.NewReader(""), "relative/path", "0644"); err == nil {
		t.Error("relative guest paths must be rejected")
	}
	if err := c.WriteFile(ctx, strings.NewReader(""), "/tmp/x", "rwx"); err == nil {
		t.Error("non-octal modes must be rejected")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("nothing should run for invalid arguments:\n%s", fake.Dump())
	}
}

func TestWriteScriptKeepsArgumentsPositional(t *testing.T) {
	for _, want := range []string{`umask 077`, `"$1.tmp.$$"`, `chmod "$2"`, `mv -f "$t" "$1"`} {
		if !strings.Contains(writeScript, want) {
			t.Errorf("writeScript should contain %q", want)
		}
	}
}

func TestErrorsAreWrapped(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, errors.New("boom") })
	c := NewClient(fake, "cracklet")
	ctx := context.Background()
	for name, err := range map[string]error{
		"create": c.Create(ctx, "/tmp/x"),
		"start":  c.Start(ctx),
		"write":  c.WriteFile(ctx, strings.NewReader(""), "/tmp/x", "0644"),
	} {
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s: expected wrapped error, got %v", name, err)
		}
	}
}

func TestStopAndEditCommandLines(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	c := NewClient(fake, "cracklet")
	ctx := context.Background()
	if err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := c.Edit(ctx, Resize{CPUs: 8, MemoryGiB: 16, DiskGiB: 80}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if err := c.Edit(ctx, Resize{MemoryGiB: 12}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	for _, want := range []string{
		"limactl stop cracklet",
		"limactl edit --tty=false --cpus 8 --memory 16 --disk 80 cracklet",
		"limactl edit --tty=false --memory 12 cracklet",
	} {
		if !fake.Called(want) {
			t.Errorf("missing call %q in:\n%s", want, fake.Dump())
		}
	}
}

func TestEditWithoutChangesRunsNothing(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	if err := NewClient(fake, "cracklet").Edit(context.Background(), Resize{}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("an empty resize must not invoke limactl:\n%s", fake.Dump())
	}
}

func TestStopAndEditWrapErrors(t *testing.T) {
	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, errors.New("boom") })
	c := NewClient(fake, "cracklet")
	ctx := context.Background()
	for name, err := range map[string]error{
		"stop": c.Stop(ctx),
		"edit": c.Edit(ctx, Resize{CPUs: 2}),
	} {
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s: expected wrapped error, got %v", name, err)
		}
	}
}
