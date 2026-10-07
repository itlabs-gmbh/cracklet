package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
	"github.com/itlabs-gmbh/cracklet/internal/grant"
)

// TestMain doubles as a tiny stdio MCP server when CRACKLET_TEST_MCP is set,
// so the bridge can be exercised against a real child process.
func TestMain(m *testing.M) {
	if os.Getenv("CRACKLET_TEST_MCP") == "1" {
		fakeMCPServer()
		return
	}
	os.Exit(m.Run())
}

func fakeMCPServer() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue
		}
		method, _ := msg["method"].(string)
		id, hasID := msg["id"]
		switch {
		case method == "ping/server":
			// A server-initiated request: expect the bridge to reject it.
			fmt.Println(`{"jsonrpc":"2.0","id":"srv-1","method":"sampling/createMessage","params":{}}`)
		case hasID:
			reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"echo": method, "env": os.Getenv("FAKE_MCP_ENV"), "host": os.Getenv("SECRET_FROM_MAC")}})
			fmt.Println(string(reply))
		default:
			fmt.Fprintln(os.Stderr, "notification:", method)
		}
	}
}

// safeBuffer is a bytes.Buffer that may be read while child stderr is still
// being copied into it.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %s", what)
}

type fakeSecrets map[string]string

func (f fakeSecrets) Resolve(_ context.Context, ref string) (string, error) {
	v, ok := f[ref]
	if !ok {
		return "", fmt.Errorf("no secret %s", ref)
	}
	return v, nil
}

func proxyCap(name, upstream string, scope int) cap.Cap {
	return cap.Cap{Name: name, Proxy: &cap.Proxy{
		Upstream: upstream, ScopeSegments: scope,
		Headers: map[string]string{"Authorization": `Bearer {{ secret "env:TOKEN" }}`, "X-Vm": "{{ .VM }}"},
	}}
}

func newBroker(t *testing.T, caps cap.Set, grants []string) (*Broker, *safeBuffer) {
	t.Helper()
	set, err := grant.ParseSet(grants)
	if err != nil {
		t.Fatal(err)
	}
	audit := &safeBuffer{}
	b := &Broker{
		VM: "agent1", Caps: caps,
		Grants:  func() (grant.Set, error) { return set, nil },
		Secrets: fakeSecrets{"env:TOKEN": "real-secret"},
		Audit:   audit,
		Data:    cap.TemplateData{VM: "agent1", BrokerURL: "http://127.0.0.1:7777", PseudoToken: "pseudo"},
		Now:     func() time.Time { return time.Unix(0, 0) },
	}
	t.Cleanup(b.Close)
	return b, audit
}

func TestProxyInjectsCredentialsAndStripsGuestHeaders(t *testing.T) {
	var seen http.Header
	var seenPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenPath = r.Header.Clone(), r.URL.Path
		w.Header().Set("X-Upstream", "yes")
		fmt.Fprint(w, "hello")
	}))
	defer upstream.Close()

	b, audit := newBroker(t, cap.Set{"claude": proxyCap("claude", upstream.URL+"/base", 0)}, []string{"claude"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/claude/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-pseudo")
	req.Header.Set("X-Api-Key", "guest-key")
	req.Header.Set("Cookie", "a=b")
	req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "claude-cli/2.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "hello" || resp.Header.Get("X-Upstream") != "yes" {
		t.Fatalf("status %d body %q headers %v", resp.StatusCode, body, resp.Header)
	}
	if seenPath != "/base/v1/messages" {
		t.Errorf("upstream path = %q", seenPath)
	}
	if got := seen.Get("Authorization"); got != "Bearer real-secret" {
		t.Errorf("Authorization = %q", got)
	}
	if seen.Get("X-Api-Key") != "" || seen.Get("Cookie") != "" {
		t.Errorf("guest credential headers leaked: %v", seen)
	}
	if seen.Get("Anthropic-Beta") != "oauth-2025-04-20" || seen.Get("User-Agent") != "claude-cli/2.1" || seen.Get("X-Vm") != "agent1" {
		t.Errorf("client headers should pass through, got %v", seen)
	}
	if seen.Get("X-Forwarded-For") != "" {
		t.Errorf("no forwarding headers should be added: %v", seen)
	}
	want := "1970-01-01T00:00:00Z vm=agent1 cap=claude scope=- allow POST \"/claude/v1/messages\" 200\n"
	if audit.String() != want {
		t.Errorf("audit = %q, want %q", audit.String(), want)
	}
}

func TestProxyAllowHeadersKeepsOnlyListed(t *testing.T) {
	var seen http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Clone() }))
	defer upstream.Close()
	c := proxyCap("strict", upstream.URL, 0)
	c.Proxy.AllowHeaders = []string{"Anthropic-Version"}
	b, _ := newBroker(t, cap.Set{"strict": c}, []string{"strict"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/strict/x", strings.NewReader("{}"))
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Stainless-Lang", "js")
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	if seen.Get("Anthropic-Version") == "" || seen.Get("Content-Type") == "" {
		t.Errorf("allowed headers missing: %v", seen)
	}
	if seen.Get("X-Stainless-Lang") != "" {
		t.Errorf("unlisted header passed through: %v", seen)
	}
	if seen.Get("Authorization") != "Bearer real-secret" {
		t.Errorf("injected header lost: %v", seen)
	}
}

func TestDenyUnknownAndUngranted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	caps := cap.Set{"claude": proxyCap("claude", upstream.URL, 0), "github": proxyCap("github", upstream.URL, 2)}
	b, audit := newBroker(t, caps, []string{"github:org/repo"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	cases := []struct {
		path   string
		status int
		hint   string
	}{
		{"/", 404, "cracklet cap ls"},
		{"/nope/x", 404, "cracklet cap ls"},
		{"/claude/v1/messages", 403, "cracklet grant agent1 claude"},
		{"/github/org/other.git/info/refs", 403, "cracklet grant agent1 github:org/other"},
		{"/github/org/repo.git/info/refs", 200, ""},
		{"/github/org/repo/git-upload-pack", 200, ""},
		{"/github/org", 403, "cracklet grant agent1 github"},
		{"/github/org/repo/../other/info/refs", 400, ""},
		{"/github/org/repo/./info/refs", 400, ""},
		{"/github/org/repo%2F..%2Fother/info/refs", 400, ""},
		{"/github/org/repo/%2e%2e/other/info/refs", 400, ""},
	}
	for _, tc := range cases {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", tc.path, resp.StatusCode, tc.status, body)
		}
		if tc.hint != "" && !strings.Contains(string(body), tc.hint) {
			t.Errorf("%s: want hint %q in %s", tc.path, tc.hint, body)
		}
	}
	if !strings.Contains(audit.String(), "cap=claude scope=- denied GET \"/claude/v1/messages\" 403") {
		t.Errorf("audit should record denials:\n%s", audit.String())
	}
	if !strings.Contains(audit.String(), "unsafe path GET \"/github/org/repo/../other/info/refs\" 400") {
		t.Errorf("audit should record rejected paths:\n%s", audit.String())
	}
}

func TestProxyReportsSecretFailure(t *testing.T) {
	c := cap.Cap{Name: "x", Proxy: &cap.Proxy{Upstream: "http://127.0.0.1:1",
		Headers: map[string]string{"Authorization": `{{ secret "keychain:cracklet/missing" }}`}}}
	b, audit := newBroker(t, cap.Set{"x": c}, []string{"x"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/x/path")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 502 || strings.Contains(string(body), "keychain:cracklet/missing") || !strings.Contains(string(body), "credential unavailable") {
		t.Errorf("guest must get a generic error: status %d body %s", resp.StatusCode, body)
	}
	if !strings.Contains(audit.String(), "no secret keychain:cracklet/missing") {
		t.Errorf("detail should be in the audit log:\n%s", audit.String())
	}
}

func TestExecPipesBodyAndMetadata(t *testing.T) {
	c := cap.Cap{Name: "tool", Exec: &cap.Exec{Command: "sh", Args: []string{"-c", `printf '%s|%s|%s|%s|' "$CRACKLET_VM" "$CRACKLET_CAP" "$CRACKLET_METHOD" "$CRACKLET_PATH"; cat`}}}
	b, _ := newBroker(t, cap.Set{"tool": c}, []string{"tool"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/tool/do/it", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "agent1|tool|POST|/do/it|payload" {
		t.Errorf("status %d body %q", resp.StatusCode, body)
	}

	failing := cap.Cap{Name: "bad", Exec: &cap.Exec{Command: "sh", Args: []string{"-c", "echo boom >&2; exit 3"}}}
	b2, _ := newBroker(t, cap.Set{"bad": failing}, []string{"bad"})
	srv2 := httptest.NewServer(b2.Handler())
	defer srv2.Close()
	resp, _ = http.Post(srv2.URL+"/bad/", "text/plain", nil)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 502 || strings.Contains(string(body), "boom") {
		t.Errorf("failing exec must not leak stderr: status %d body %s", resp.StatusCode, body)
	}

	leaky := cap.Cap{Name: "env", Exec: &cap.Exec{Command: "sh", Args: []string{"-c", "echo \"${SECRET_FROM_MAC:-clean}\""}}}
	t.Setenv("SECRET_FROM_MAC", "leaked")
	b3, _ := newBroker(t, cap.Set{"env": leaky}, []string{"env"})
	srv3 := httptest.NewServer(b3.Handler())
	defer srv3.Close()
	resp, _ = http.Post(srv3.URL+"/env/", "text/plain", nil)
	body, _ = io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != "clean" {
		t.Errorf("host environment must not reach exec programs, got %q", body)
	}
}

func mcpCap() cap.Cap {
	return cap.Cap{Name: "fake", MCP: &cap.MCP{Command: os.Args[0], Env: map[string]string{"CRACKLET_TEST_MCP": "1", "FAKE_MCP_ENV": "set"}}}
}

func rpc(t *testing.T, url, msg string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestMCPBridgeAnswersRequestsAndAcceptsNotifications(t *testing.T) {
	t.Setenv("SECRET_FROM_MAC", "leaked")
	b, audit := newBroker(t, cap.Set{"fake": mcpCap()}, []string{"fake"})
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()
	url := srv.URL + "/fake"

	resp, body := rpc(t, url, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"echo":"initialize"`) || !strings.Contains(body, `"env":"set"`) {
		t.Fatalf("initialize: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `"host":""`) {
		t.Errorf("host environment must not reach the MCP child: %s", body)
	}
	if resp.Header.Get("Mcp-Session-Id") == "" || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("initialize response headers: %v", resp.Header)
	}
	resp, _ = rpc(t, url, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if resp.StatusCode != 202 {
		t.Errorf("notification status %d", resp.StatusCode)
	}
	resp, body = rpc(t, url, `{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"id":"abc"`) {
		t.Errorf("tools/list: %d %s", resp.StatusCode, body)
	}
	// Two clients using the same id must each get their own answer.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			method := fmt.Sprintf("m%d", i)
			resp, body := rpc(t, url, `{"jsonrpc":"2.0","id":1,"method":"`+method+`"}`)
			if resp.StatusCode != 200 || !strings.Contains(body, `"echo":"`+method+`"`) || !strings.Contains(body, `"id":1`) {
				t.Errorf("client %d: %d %s", i, resp.StatusCode, body)
			}
		}(i)
	}
	wg.Wait()
	// The child emits a server-initiated request; the bridge must reject it
	// without hanging and keep serving.
	resp, _ = rpc(t, url, `{"jsonrpc":"2.0","method":"ping/server"}`)
	if resp.StatusCode != 202 {
		t.Errorf("ping/server status %d", resp.StatusCode)
	}
	resp, body = rpc(t, url, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"id":2`) {
		t.Errorf("after server request: %d %s", resp.StatusCode, body)
	}
	for _, bad := range []string{`[]`, `not json`, ``} {
		if resp, _ := rpc(t, url, bad); resp.StatusCode != 400 {
			t.Errorf("%q: status %d, want 400", bad, resp.StatusCode)
		}
	}
	if resp, err := http.Get(url); err != nil || resp.StatusCode != 405 {
		t.Errorf("GET should be 405, got %v %v", resp, err)
	}
	waitFor(t, "child stderr in the log", func() bool {
		return strings.Contains(audit.String(), "mcp fake: notification: notifications/initialized")
	})
}

func TestListenAndServeOnUnixSocket(t *testing.T) {
	// macOS limits socket paths to 104 bytes; t.TempDir() is longer than that.
	dir, err := os.MkdirTemp("/tmp", "cracklet-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "run", "vm.sock")
	if Alive(sock) {
		t.Fatalf("socket should not be alive before Listen")
	}
	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if _, err := Listen(strings.Repeat("x", 120)); err == nil {
		t.Errorf("overlong socket path must be rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	}()
	if !Alive(sock) {
		t.Errorf("socket should be alive while serving")
	}
	if _, err := Listen(sock); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Errorf("second Listen should fail, got %v", err)
	}
	client := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (netConn, error) {
		return dialUnix(sock)
	}}}
	resp, err := client.Get("http://broker/")
	if err != nil {
		t.Fatalf("GET via socket: %v", err)
	}
	if body, _ := io.ReadAll(resp.Body); string(body) != "ok" {
		t.Errorf("body = %q", body)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve: %v", err)
	}
	if info, err := os.Stat(sock); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %o", info.Mode().Perm())
	}
}
