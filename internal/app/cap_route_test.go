package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

const routedBody = `name = "forge"
[proxy]
upstream = "https://forge.example"
[[proxy.routes]]
host = "api.forge.example"
upstream = "https://api.forge.example"
[proxy.routes.headers]
Authorization = "token {{ secret \"keychain:cracklet/forge-api\" }}"
`

func TestCapSummaryNamesRoutes(t *testing.T) {
	c, err := cap.Parse(routedBody, "t")
	if err != nil {
		t.Fatal(err)
	}
	got := capSummary(c)
	for _, want := range []string{
		"proxies guest requests to https://forge.example",
		"answers requests for api.forge.example with https://api.forge.example",
		"sends the secret keychain:cracklet/forge-api in the Authorization header",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
}

func TestCapAddRejectsClaimedHost(t *testing.T) {
	// The embedded github cap routes api.github.com; a download must not
	// take it over, and must not leave a file that breaks every later Load.
	body := strings.ReplaceAll(routedBody, "api.forge.example", "api.github.com")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	http.DefaultClient = srv.Client()
	defer func() { http.DefaultClient = &http.Client{} }()

	app, _, _ := newCapApp(t)
	err := app.CapAdd(context.Background(), srv.URL+"/forge.toml", true, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "api.github.com") {
		t.Fatalf("a cap routing an already routed host must be rejected, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(app.paths.CapsDir(), "forge.toml")); statErr == nil {
		t.Errorf("rejected cap must not be installed")
	}
}

// pausingReader answers a confirmation prompt only when told to, so a test
// can run another installation while the first one waits for the user.
type pausingReader struct {
	asked  chan struct{}
	answer chan string
}

func (p *pausingReader) Read(b []byte) (int, error) {
	close(p.asked)
	return copy(b, <-p.answer), nil
}

func TestCapAddRechecksHostsAfterConfirmation(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := routedBody
		if r.URL.Path == "/forge2.toml" {
			body = strings.Replace(routedBody, `name = "forge"`, `name = "forge2"`, 1)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	http.DefaultClient = srv.Client()
	defer func() { http.DefaultClient = &http.Client{} }()

	fake := runner.NewFake(func(string, []string) ([]byte, error) { return nil, nil })
	paths := testPaths(t)
	first := New(fake, paths, &strings.Builder{})
	second := New(fake, paths, &strings.Builder{})

	confirm := &pausingReader{asked: make(chan struct{}), answer: make(chan string)}
	done := make(chan error, 1)
	go func() { done <- first.CapAdd(context.Background(), srv.URL+"/forge.toml", false, confirm) }()
	<-confirm.asked
	if err := second.CapAdd(context.Background(), srv.URL+"/forge2.toml", true, strings.NewReader("")); err != nil {
		t.Fatalf("second install: %v", err)
	}
	confirm.answer <- "y\n"
	if err := <-done; err == nil || !strings.Contains(err.Error(), "api.forge.example") {
		t.Fatalf("the first install must notice the host is taken by now, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.CapsDir(), "forge.toml")); err == nil {
		t.Errorf("both caps installed; every later Load would fail")
	}
	if _, err := cap.Load(paths.CapsDir()); err != nil {
		t.Errorf("installed caps must still load: %v", err)
	}
}
