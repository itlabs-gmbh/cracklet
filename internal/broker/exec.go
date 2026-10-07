package broker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
)

// execTimeout bounds one invocation of an exec capability.
const execTimeout = 2 * time.Minute

// maxExecBody caps what is piped into the program and what it may answer.
const maxExecBody = 8 << 20

// maxExecConcurrency bounds how many exec programs a guest can run at once.
const maxExecConcurrency = 4

// newExec wraps an external program: the request body goes to stdin, stdout
// becomes the response body. Request metadata arrives in CRACKLET_* variables.
func (b *Broker) newExec(c cap.Cap) http.Handler {
	slots := make(chan struct{}, maxExecConcurrency)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-r.Context().Done():
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxExecBody+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "read request: "+err.Error(), "")
			return
		}
		if len(body) > maxExecBody {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large", "")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), execTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, c.Exec.Command, c.Exec.Args...)
		cmd.Env = append(childEnv(nil),
			"CRACKLET_VM="+b.VM,
			"CRACKLET_CAP="+c.Name,
			"CRACKLET_SCOPE="+scopeOf(c, r.URL.Path),
			"CRACKLET_METHOD="+r.Method,
			"CRACKLET_PATH="+r.URL.Path,
			"CRACKLET_QUERY="+r.URL.RawQuery,
			"CRACKLET_CONTENT_TYPE="+r.Header.Get("Content-Type"),
		)
		cmd.Stdin = bytes.NewReader(body)
		stdout := &limitedBuffer{limit: maxExecBody}
		stderr := &limitedBuffer{limit: 64 << 10}
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		isolateChild(cmd)
		cancelGroup(cmd)
		err = cmd.Run()
		if err == nil && stdout.truncated {
			err = fmt.Errorf("output exceeds %d bytes", maxExecBody)
		}
		if err != nil {
			b.detail("cap %s: %s failed: %v: %s", c.Name, c.Exec.Command, err, lastLine(stderr.String()))
			writeError(w, http.StatusBadGateway, c.Name+": the program failed on the host; see the audit log there", "")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(stdout.buf.Bytes())
	})
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// limitedBuffer keeps at most limit bytes and remembers whether more arrived.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := l.limit - l.buf.Len(); n > room {
		l.truncated = true
		p = p[:room]
	}
	l.buf.Write(p)
	return n, nil
}

func (l *limitedBuffer) String() string { return l.buf.String() }
