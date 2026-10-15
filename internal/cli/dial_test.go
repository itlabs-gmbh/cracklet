package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
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
	if _, _, err := run(t, "_dial", "vm1.cracklet", "abc"); err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("want invalid port, got %v", err)
	}
	if _, _, err := run(t, "_dial", "vm1.cracklet"); err == nil {
		t.Error("_dial needs HOST and PORT")
	}
}

func TestDialOnLimaExplainsItself(t *testing.T) {
	_, fake, err := run(t, "_dial", "vm1.cracklet", "22")
	if !errors.Is(err, backend.ErrNoDial) {
		t.Fatalf("the Lima backend has no direct path, got %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("no command may run for an unsupported dial:\n%s", fake.Dump())
	}
}
