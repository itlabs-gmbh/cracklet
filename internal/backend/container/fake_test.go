package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/backend"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/host"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// fakeContainer simulates the `container` CLI: a system service that may be
// down, machines with states and addresses, an image store with labels.
type fakeContainer struct {
	mu       sync.Mutex
	down     bool
	version  string
	machines map[string]*machine
	labels   map[string]string // labels of ImageRef; nil = image missing
	nextIP   int
	failing  map[string]error // joined command prefix -> error
	sshUp    map[string]bool  // machines whose sshd answers
}

func newFake() *fakeContainer {
	return &fakeContainer{version: "1.5.0", machines: map[string]*machine{}, nextIP: 2,
		failing: map[string]error{}, sshUp: map[string]bool{}}
}

func (f *fakeContainer) handle(name string, args []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	joined := name + " " + strings.Join(args, " ")
	for prefix, err := range f.failing {
		if strings.HasPrefix(joined, prefix) {
			return nil, err
		}
	}
	if name == "brew" {
		return nil, nil // brew install container
	}
	if name != "container" {
		return nil, errors.New("unexpected command: " + joined)
	}
	switch {
	case joined == "container --version":
		return []byte("container CLI version " + f.version + " (build: release, commit: abc)\n"), nil
	case joined == "container system status":
		if f.down {
			return nil, errors.New("apiserver is not running")
		}
		return []byte("FIELD   VALUE\nstatus  running\nserver.version " + f.version + "\n"), nil
	case strings.HasPrefix(joined, "container system start"):
		f.down = false
		return nil, nil
	case joined == "container image inspect "+ImageRef:
		if f.labels == nil {
			return nil, errors.New("image not found")
		}
		return json.Marshal([]map[string]any{{"variants": []map[string]any{{"config": map[string]any{"config": map[string]any{"Labels": f.labels}}}}}})
	case strings.HasPrefix(joined, "container build "):
		f.labels = map[string]string{labelRev: imageRev, labelKey: "built"}
		return nil, nil
	case joined == "container machine ls --format json":
		list := make([]machine, 0, len(f.machines))
		for _, m := range f.machines {
			list = append(list, *m)
		}
		return json.Marshal(list)
	case strings.HasPrefix(joined, "container machine create "):
		n := args[indexOf(args, "--name")+1]
		if _, exists := f.machines[n]; exists {
			return nil, errors.New("machine exists")
		}
		cpus, mem := 0, int64(0)
		fmt.Sscanf(args[indexOf(args, "--cpus")+1], "%d", &cpus)
		fmt.Sscanf(args[indexOf(args, "--memory")+1], "%dM", &mem)
		f.machines[n] = &machine{ID: n, IP: fmt.Sprintf("192.168.64.%d", f.nextIP), Status: "running", CPUs: cpus, Memory: mem << 20}
		f.nextIP++
		f.sshUp[n] = true
		return nil, nil
	case strings.HasPrefix(joined, "container machine run "):
		n := args[indexOf(args, "--name")+1]
		m, ok := f.machines[n]
		if !ok {
			return nil, errors.New("no such machine")
		}
		m.Status, m.IP = "running", fmt.Sprintf("192.168.64.%d", f.nextIP)
		f.nextIP++
		f.sshUp[n] = true
		return nil, nil
	case strings.HasPrefix(joined, "container machine stop "):
		m, ok := f.machines[args[2]]
		if !ok {
			return nil, errors.New("no such machine")
		}
		m.Status, m.IP = "stopped", ""
		f.sshUp[args[2]] = false
		return nil, nil
	case strings.HasPrefix(joined, "container machine rm "):
		if _, ok := f.machines[args[2]]; !ok {
			return nil, errors.New("no such machine")
		}
		delete(f.machines, args[2])
		return nil, nil
	}
	return nil, errors.New("unexpected command: " + joined)
}

func indexOf(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// dialer answers only for machines whose sshd is up; it returns a loopback
// pipe so callers can Close it.
func (f *fakeContainer) dialer(ctx context.Context, addr string) (backend.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for n, m := range f.machines {
		if strings.HasPrefix(addr, m.IP+":") && m.IP != "" && f.sshUp[n] {
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return pipeConn{c1}, nil
		}
	}
	return nil, errors.New("connection refused")
}

type pipeConn struct{ net.Conn }

func (pipeConn) CloseWrite() error { return nil }

func goodFacts(context.Context) host.Facts {
	return host.Facts{OS: "darwin", Arch: "arm64", MacOSVersion: "26.7.1", Chip: "Apple M4 Pro", HVSupport: true, HasBrew: true, HasContainer: true}
}

func newTestBackend(t *testing.T, f *fakeContainer, opts ...Option) (*Backend, *runner.Fake, *bytes.Buffer) {
	t.Helper()
	fake := runner.NewFake(f.handle)
	out := &bytes.Buffer{}
	paths := config.Paths{Home: t.TempDir()}
	if err := os.WriteFile(paths.PubKeyPath(), []byte("ssh-ed25519 AAAA test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.KeyPath(), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts = append([]Option{WithFacts(goodFacts), WithDialer(f.dialer), WithExecutable("/usr/local/bin/cracklet"),
		WithSSHWait(2*time.Second, 5*time.Millisecond)}, opts...)
	return New(fake, paths, out, out, opts...), fake, out
}
