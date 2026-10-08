package broker

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
)

// routedCap is a github-like cap: git under /forge/, the API by Host.
func routedCap(gitUpstream, apiUpstream string) cap.Cap {
	c := proxyCap("forge", gitUpstream)
	c.Proxy.Routes = []cap.Route{{
		Host:     "api.forge.example",
		Upstream: apiUpstream,
		Headers:  map[string]string{"Authorization": `token {{ secret "env:TOKEN" }}`},
	}}
	return c
}

// hostRequest sends what gh sends over http_unix_socket: the real host name
// and path, in cleartext, with its placeholder token.
func hostRequest(t *testing.T, srv *httptest.Server, method, host, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	req.Header.Set("Authorization", "token placeholder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestRouteByHostInjectsRouteHeaders(t *testing.T) {
	var seenAuth, seenPath, seenHost string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth, seenPath, seenHost = r.Header.Get("Authorization"), r.URL.Path, r.Host
		fmt.Fprint(w, "api")
	}))
	defer api.Close()
	git := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("git upstream must not see API calls: %s", r.URL.Path)
	}))
	defer git.Close()

	b, audit := newBroker(t, cap.Set{"forge": routedCap(git.URL, api.URL)}, []string{"forge"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	resp, body := hostRequest(t, srv, http.MethodGet, "api.forge.example", "/repos/org/repo/pulls")
	if resp.StatusCode != 200 || body != "api" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if seenAuth != "token real-secret" {
		t.Errorf("Authorization = %q, want the route's header with the real secret", seenAuth)
	}
	if seenPath != "/repos/org/repo/pulls" {
		t.Errorf("upstream path = %q", seenPath)
	}
	if seenHost == "api.forge.example" {
		t.Errorf("upstream Host must be the upstream's, got %q", seenHost)
	}
	want := "1970-01-01T00:00:00Z vm=agent1 cap=forge allow GET \"/repos/org/repo/pulls\" 200\n"
	waitFor(t, "audit line", func() bool { return audit.String() == want })
}

// A routed host belongs to its capability: the one grant covers every path
// on it, and without the grant every path is denied with the grant command.
func TestRouteGrants(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer api.Close()
	caps := cap.Set{"forge": routedCap(api.URL, api.URL)}

	for _, tc := range []struct {
		grants []string
		method string
		host   string
		path   string
		status int
	}{
		{[]string{"forge"}, "GET", "api.forge.example", "/repos/org/repo", 200},
		{[]string{"forge"}, "POST", "api.forge.example", "/graphql", 200},
		{[]string{"forge"}, "GET", "api.forge.example", "/user", 200},
		{[]string{"forge"}, "POST", "api.forge.example", "/repos/org/repo/forks", 200},
		// A port in the Host header still selects the route.
		{[]string{"forge"}, "GET", "api.forge.example:443", "/user", 200},
		{[]string{"forge"}, "GET", "API.FORGE.EXAMPLE", "/repos/org/repo", 200},
		// Unknown hosts fall back to /<cap>/ routing.
		{[]string{"forge"}, "GET", "elsewhere.example", "/user", 404},
		{[]string{"forge"}, "GET", "elsewhere.example", "/forge/org/repo/info/refs", 200},
		{nil, "GET", "api.forge.example", "/user", 403},
		{nil, "POST", "api.forge.example", "/graphql", 403},
	} {
		b, _ := newBroker(t, caps, tc.grants)
		srv := httptest.NewServer(b.Handler())
		resp, body := hostRequest(t, srv, tc.method, tc.host, tc.path)
		srv.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%v %s %s%s: status %d, want %d (%s)", tc.grants, tc.method, tc.host, tc.path, resp.StatusCode, tc.status, body)
		}
		if tc.status == http.StatusForbidden && !strings.Contains(body, "cracklet grant agent1 forge") {
			t.Errorf("%s%s: denial should name the grant, got %s", tc.host, tc.path, body)
		}
	}
}

func TestRouteStillRejectsUnsafePaths(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unsafe path reached upstream: %s", r.URL.Path)
	}))
	defer api.Close()
	b, _ := newBroker(t, cap.Set{"forge": routedCap(api.URL, api.URL)}, []string{"forge"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	resp, _ := hostRequest(t, srv, "GET", "api.forge.example", "/repos/org/repo/%2e%2e/other")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
}
