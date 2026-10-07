package cli

import (
	"strings"
	"testing"
)

func TestExecQuotesEveryArgument(t *testing.T) {
	_, fake, err := run(t, "exec", "vm1", "--", "sh", "-c", "echo $HOME | wc -c", "it's", "")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	want := ` vm1.cracklet -- 'sh' -c 'echo $HOME | wc -c' 'it'\''s' ''`
	if !fake.CalledWithSuffix(want) {
		t.Errorf("argv not quoted exactly, want suffix %q:\n%s", want, fake.Dump())
	}
}

func TestExecWithoutSeparatorKeepsFlagsForRemote(t *testing.T) {
	_, fake, err := run(t, "exec", "vm1", "ls", "-la")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !fake.CalledWithSuffix(" vm1.cracklet -- 'ls' -la") {
		t.Errorf("remote flags must reach the guest:\n%s", fake.Dump())
	}
}

func TestExecKeepsSecondSeparatorAsArgument(t *testing.T) {
	_, fake, err := run(t, "exec", "vm1", "--", "printf", "%s", "--")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !fake.CalledWithSuffix(" vm1.cracklet -- 'printf' %s --") {
		t.Errorf("only the first -- belongs to cracklet:\n%s", fake.Dump())
	}
}

func TestExecRequiresCommand(t *testing.T) {
	for _, args := range [][]string{{"exec", "vm1"}, {"exec", "vm1", "--"}} {
		_, fake, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "command") {
			t.Errorf("%v: expected missing-command error, got %v", args, err)
		}
		for _, c := range fake.Calls() {
			if strings.HasPrefix(c, "ssh ") {
				t.Errorf("%v: ssh must not run without a command: %s", args, c)
			}
		}
	}
}

func TestExecRejectsInvalidName(t *testing.T) {
	if _, _, err := run(t, "exec", "../x", "--", "true"); err == nil {
		t.Fatal("expected invalid-name error")
	}
}

// ssh keeps plain-ssh semantics: the words form a shell command line.
func TestSSHLeavesShellSyntaxToGuest(t *testing.T) {
	_, fake, err := run(t, "ssh", "vm1", "echo $HOME | wc -c")
	if err != nil {
		t.Fatalf("ssh: %v", err)
	}
	if !fake.CalledWithSuffix(" vm1.cracklet -- echo $HOME | wc -c") {
		t.Errorf("ssh must not quote its command:\n%s", fake.Dump())
	}
}
