package broker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
)

const (
	// mcpResponseTimeout bounds how long a JSON-RPC request may take.
	mcpResponseTimeout = 5 * time.Minute
	// mcpMaxLine is the largest JSON-RPC line accepted from either side.
	mcpMaxLine = 32 << 20
	// mcpSessionHeader is the MCP Streamable HTTP session header.
	mcpSessionHeader = "Mcp-Session-Id"
)

// mcpBridge exposes a stdio MCP server as a Streamable HTTP endpoint: every
// POST carries one JSON-RPC message, requests are answered with the matching
// response, notifications with 202. Server-initiated requests are not
// supported (GET answers 405, as the protocol allows).
//
// Request IDs are rewritten to bridge-unique values on the way in and mapped
// back on the way out, so several guest clients cannot collide.
type mcpBridge struct {
	spec cap.Cap
	log  io.Writer

	// mu guards the process state and the pending table.
	mu      sync.Mutex
	cmd     *exec.Cmd
	running bool
	stdin   io.WriteCloser
	pending map[uint64]pendingRequest
	nextID  uint64
	session string

	// writeSlot serialises writes to the child's stdin and is never held
	// together with mu, so a blocked write cannot stall the read loop. It is
	// a channel rather than a mutex so waiting for it honours the request
	// context.
	writeSlot chan struct{}
}

type pendingRequest struct {
	clientID json.RawMessage
	reply    chan json.RawMessage
}

func newMCPBridge(c cap.Cap, log io.Writer) *mcpBridge {
	return &mcpBridge{spec: c, log: log, pending: map[uint64]pendingRequest{}, writeSlot: make(chan struct{}, 1)}
}

func (b *mcpBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		b.post(w, r)
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "this MCP bridge only accepts POST", "")
	}
}

func (b *mcpBridge) post(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpMaxLine))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read request: "+err.Error(), "")
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] == '[' {
		writeError(w, http.StatusBadRequest, "expected a single JSON-RPC message", "")
		return
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON-RPC message: "+err.Error(), "")
		return
	}
	if err := b.ensureRunning(); err != nil {
		b.logf("start failed: %v", err)
		writeError(w, http.StatusBadGateway, b.spec.Name+": the MCP server could not be started on the host; see the audit log there", "")
		return
	}
	clientID, hasID := msg["id"]
	method := string(bytes.Trim(msg["method"], `"`))
	isRequest := hasID && string(clientID) != "null" && len(msg["method"]) > 0
	// The deadline covers the write as well: a child that stopped reading
	// its stdin must not pin the request (or later writers) forever.
	ctx, cancel := context.WithTimeout(r.Context(), mcpResponseTimeout)
	defer cancel()
	if !isRequest {
		// Re-encode so pretty-printed JSON reaches the line-based child as one line.
		compact, err := json.Marshal(msg)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON-RPC message: "+err.Error(), "")
			return
		}
		if err := b.send(ctx, compact); err != nil {
			writeError(w, http.StatusBadGateway, b.spec.Name+": the MCP server is not accepting messages", "")
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}
	id, reply := b.register(clientID)
	rewritten, err := withID(msg, id)
	if err != nil {
		b.unregister(id)
		writeError(w, http.StatusBadRequest, "invalid JSON-RPC message: "+err.Error(), "")
		return
	}
	if err := b.send(ctx, rewritten); err != nil {
		b.unregister(id)
		writeError(w, http.StatusBadGateway, b.spec.Name+": the MCP server is not accepting messages", "")
		return
	}
	select {
	case resp := <-reply:
		w.Header().Set("Content-Type", "application/json")
		if method == "initialize" {
			w.Header().Set(mcpSessionHeader, b.sessionID())
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp)
	case <-ctx.Done():
		b.unregister(id)
		writeError(w, http.StatusGatewayTimeout, fmt.Sprintf("%s: no response for %s", b.spec.Name, method), "")
	}
}

// withID re-encodes a message with a different id. Marshalling a map sorts
// the keys; JSON-RPC does not care about order.
func withID(msg map[string]json.RawMessage, id any) ([]byte, error) {
	out := make(map[string]json.RawMessage, len(msg))
	for k, v := range msg {
		out[k] = v
	}
	raw, err := json.Marshal(id)
	if err != nil {
		return nil, err
	}
	out["id"] = raw
	return json.Marshal(out)
}

func (b *mcpBridge) ensureRunning() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return nil
	}
	cmd := exec.Command(b.spec.MCP.Command, b.spec.MCP.Args...)
	cmd.Env = childEnv(b.spec.MCP.Env)
	isolateChild(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = b.stderr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", b.spec.MCP.Command, err)
	}
	b.cmd, b.stdin, b.running = cmd, stdin, true
	go b.readLoop(cmd, stdout)
	return nil
}

func (b *mcpBridge) stderr() io.Writer {
	if b.log == nil {
		return io.Discard
	}
	return prefixWriter{w: b.log, prefix: "mcp " + b.spec.Name + ": "}
}

func (b *mcpBridge) logf(format string, args ...any) {
	if b.log != nil {
		fmt.Fprintf(b.log, "mcp "+b.spec.Name+": "+format+"\n", args...)
	}
}

// readLoop delivers responses to waiting requests and marks the child as
// gone when stdout closes. A line beyond mcpMaxLine ends the session: the
// child is killed rather than left alive with nobody reading it.
func (b *mcpBridge) readLoop(cmd *exec.Cmd, stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), mcpMaxLine)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if len(msg["method"]) > 0 {
			go b.rejectServerRequest(msg["id"])
			continue
		}
		b.deliver(msg)
	}
	if err := sc.Err(); err != nil {
		b.logf("read: %v", err)
		_ = killGroup(cmd)
	}
	_ = cmd.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cmd == cmd {
		b.running = false
		b.failPending(fmt.Errorf("mcp server exited"))
	}
}

// deliver hands a response to its waiting request with the client's id restored.
func (b *mcpBridge) deliver(msg map[string]json.RawMessage) {
	id, err := strconv.ParseUint(string(msg["id"]), 10, 64)
	if err != nil {
		return
	}
	b.mu.Lock()
	p, ok := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if !ok {
		return
	}
	out := make(map[string]json.RawMessage, len(msg))
	for k, v := range msg {
		out[k] = v
	}
	out["id"] = p.clientID
	raw, err := json.Marshal(out)
	if err != nil {
		return
	}
	p.reply <- raw
}

// rejectServerRequest answers a server-initiated request (sampling,
// elicitation) with a JSON-RPC error so the child does not wait forever.
func (b *mcpBridge) rejectServerRequest(id json.RawMessage) {
	if len(id) == 0 || string(id) == "null" {
		return // a notification; nothing to answer
	}
	msg, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": -32601, "message": "server-initiated requests are not supported by the cracklet bridge"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), mcpResponseTimeout)
	defer cancel()
	_ = b.send(ctx, msg)
}

// send writes one line to the child. If ctx ends while waiting for the
// write slot or during the write, the child is wedged and gets killed: a
// stuck stdin write cannot be interrupted any other way, and the read loop
// then reports the exit to every pending request.
func (b *mcpBridge) send(ctx context.Context, msg []byte) error {
	b.mu.Lock()
	cmd, stdin, running := b.cmd, b.stdin, b.running
	b.mu.Unlock()
	if !running || stdin == nil {
		return fmt.Errorf("%s: mcp server is not running", b.spec.Name)
	}
	select {
	case b.writeSlot <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("%s: mcp server is not reading its input", b.spec.Name)
	}
	done := make(chan error, 1)
	go func() {
		defer func() { <-b.writeSlot }()
		_, err := stdin.Write(append(append([]byte(nil), msg...), '\n'))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: write to mcp server: %w", b.spec.Name, err)
		}
		return nil
	case <-ctx.Done():
		b.logf("write stalled, killing the server")
		_ = killGroup(cmd)
		<-done
		return fmt.Errorf("%s: mcp server stopped reading its input", b.spec.Name)
	}
}

func (b *mcpBridge) register(clientID json.RawMessage) (uint64, chan json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	ch := make(chan json.RawMessage, 1)
	b.pending[b.nextID] = pendingRequest{clientID: append(json.RawMessage(nil), clientID...), reply: ch}
	return b.nextID, ch
}

func (b *mcpBridge) unregister(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pending, id)
}

// failPending answers every waiting request with a JSON-RPC error. Caller holds mu.
func (b *mcpBridge) failPending(cause error) {
	for id, p := range b.pending {
		msg, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": p.clientID,
			"error": map[string]any{"code": -32000, "message": cause.Error()},
		})
		p.reply <- msg
		delete(b.pending, id)
	}
}

func (b *mcpBridge) sessionID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			raw = []byte(time.Now().Format(time.RFC3339Nano))
		}
		b.session = hex.EncodeToString(raw)
	}
	return b.session
}

func (b *mcpBridge) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stdin != nil {
		_ = b.stdin.Close()
	}
	if b.running {
		_ = killGroup(b.cmd)
	}
	b.failPending(fmt.Errorf("broker closed"))
}

type prefixWriter struct {
	w      io.Writer
	prefix string
}

func (p prefixWriter) Write(data []byte) (int, error) {
	for _, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n")) {
		fmt.Fprintf(p.w, "%s%s\n", p.prefix, line)
	}
	return len(data), nil
}
