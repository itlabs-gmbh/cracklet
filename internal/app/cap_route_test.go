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
