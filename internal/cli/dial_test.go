package cli

import (
	"strings"
	"testing"
)

func TestDialIsHiddenAndValidatesPort(t *testing.T) {
	root := newRoot(nil)
	var found bool
	for _, c := range root.Commands() {
		if c.Name() == "_dial" {
			found = true
			if !c.Hidden {
				t.Error("_dial must be hidden from help: it is ssh's ProxyCommand, not a user command")
			}
			if c.Annotations[annotationStdoutIsData] == "" {
				t.Error("_dial must mark stdout as data so notes go to stderr")
			}
		}
	}
	if !found {
		t.Fatal("_dial not registered")
	}
	if _, _, err := run(t, "_dial", "vm1.skiff", "abc"); err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("want invalid port, got %v", err)
	}
	if _, _, err := run(t, "_dial", "vm1.skiff"); err == nil {
		t.Error("_dial needs HOST and PORT")
	}
}

func TestDialAsksTheBackend(t *testing.T) {
	_, fb, err := run(t, "_dial", "vm1.skiff", "22")
	if err == nil || !strings.Contains(err.Error(), "vm1:22") || !fb.Called("dial vm1 22") {
		t.Fatalf("want the backend's dial error naming the target, got %v\n%s", err, fb.Dump())
	}
}

func TestRelayCommandIsHiddenAndTakesNoArgs(t *testing.T) {
	cmd, _, err := newRoot(nil).Find([]string{"_relay"})
	if err != nil || cmd == nil || !cmd.Hidden {
		t.Fatalf("_relay must exist and be hidden: %v", err)
	}
	if err := cmd.Args(cmd, []string{"extra"}); err == nil {
		t.Error("_relay takes no arguments")
	}
	if _, _, err := run(t, "_relay"); err == nil || !strings.Contains(err.Error(), "relay") {
		t.Errorf("a backend without a relay must be refused clearly, got %v", err)
	}
}
