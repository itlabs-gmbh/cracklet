package lima

import (
	"context"
	"errors"
	"testing"

	"github.com/itlabs-gmbh/skiff/internal/backend"
)

func TestSSHProxyCommandHopsThroughLima(t *testing.T) {
	b := newTestBackend(t)
	want := "ssh -F /tmp/lima/skiff/ssh.config -W %h:%p lima-skiff"
	if got := b.SSHProxyCommand(); got != want {
		t.Errorf("SSHProxyCommand = %q, want %q", got, want)
	}
}

func TestDialIsNotSupported(t *testing.T) {
	b := newTestBackend(t)
	if _, err := b.Dial(context.Background(), "vm1", 22); !errors.Is(err, backend.ErrNoDial) {
		t.Errorf("Dial = %v, want ErrNoDial", err)
	}
}

func TestLimaPinsNoHostKeys(t *testing.T) {
	b := newTestBackend(t)
	if got := b.KnownHostsFile(); got != "" {
		t.Errorf("KnownHostsFile = %q; Lima guests share an image-wide host key", got)
	}
}
