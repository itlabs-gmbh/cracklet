package envd

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const good = `{"ip":"172.16.3.2","prefix":30,"gateway":"172.16.3.1","hostname":"dev-1","unix_time":1700000000,"seed":"AAEC"}`

type fakeSystem struct {
	calls []string
	fail  string
}

func (f *fakeSystem) record(call string) error {
	f.calls = append(f.calls, call)
	if f.fail != "" && strings.HasPrefix(call, f.fail) {
		return errors.New("injected")
	}
	return nil
}

func (f *fakeSystem) ReplaceAddress(iface, cidr string) error {
	return f.record("addr " + iface + " " + cidr)
}
func (f *fakeSystem) ReplaceDefaultRoute(iface, gw string) error {
	return f.record("route " + iface + " " + gw)
}
func (f *fakeSystem) SetHostname(name string) error { return f.record("hostname " + name) }
func (f *fakeSystem) SetTime(unix int64) error      { return f.record("time") }

func TestParse(t *testing.T) {
	id, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if id.Iface != "eth0" || id.Hostname != "dev-1" || id.CIDR() != "172.16.3.2/30" {
		t.Errorf("unexpected identity %+v", id)
	}
	bad := []string{
		`{`,
		`{"ip":"nope","prefix":30,"gateway":"172.16.3.1","hostname":"a","unix_time":1}`,
		`{"ip":"172.16.3.2","prefix":0,"gateway":"172.16.3.1","hostname":"a","unix_time":1}`,
		`{"ip":"172.16.3.2","prefix":30,"gateway":"172.16.3.1","hostname":"Bad Host","unix_time":1}`,
		`{"ip":"172.16.3.2","prefix":30,"gateway":"172.16.3.1","hostname":"a","unix_time":0}`,
		`{"ip":"172.16.3.2","prefix":30,"gateway":"172.16.3.1","hostname":"a","unix_time":1,"seed":"***"}`,
	}
	for _, b := range bad {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("Parse(%s) expected error", b)
		}
	}
}

func TestApplyInOrder(t *testing.T) {
	id, _ := Parse([]byte(good))
	sys := &fakeSystem{}
	dir := t.TempDir()
	hostFile, randFile := filepath.Join(dir, "hostname"), filepath.Join(dir, "urandom")
	if err := Apply(id, sys, hostFile, randFile); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := "addr eth0 172.16.3.2/30\nroute eth0 172.16.3.1\nhostname dev-1\ntime"
	if got := strings.Join(sys.calls, "\n"); got != want {
		t.Errorf("calls:\n%s\nwant:\n%s", got, want)
	}
	if h, _ := os.ReadFile(hostFile); string(h) != "dev-1\n" {
		t.Errorf("hostname file = %q", h)
	}
	seed, _ := base64.StdEncoding.DecodeString("AAEC")
	if r, _ := os.ReadFile(randFile); string(r) != string(seed) {
		t.Errorf("seed not written: %q", r)
	}
}

func TestApplyStopsOnFailure(t *testing.T) {
	id, _ := Parse([]byte(good))
	sys := &fakeSystem{fail: "route"}
	err := Apply(id, sys, filepath.Join(t.TempDir(), "h"), filepath.Join(t.TempDir(), "r"))
	if err == nil || !strings.Contains(err.Error(), "route") || len(sys.calls) != 2 {
		t.Fatalf("expected route failure to abort, err=%v calls=%v", err, sys.calls)
	}
}
