package broker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// shutdownGrace is how long in-flight requests get when the tunnel closes.
const shutdownGrace = 2 * time.Second

// Alive reports whether a broker already answers on the socket.
func Alive(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// maxSocketPath is the longest Unix socket path macOS accepts (sun_path is 104 bytes).
const maxSocketPath = 100

// Listen binds the Unix socket, replacing a stale file from a previous run.
func Listen(socketPath string) (net.Listener, error) {
	if len(socketPath) > maxSocketPath {
		return nil, fmt.Errorf("socket path %s is longer than %d characters; set CRACKLET_HOME to a shorter path", socketPath, maxSocketPath)
	}
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	// MkdirAll leaves the mode of an existing directory alone.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("restrict socket directory: %w", err)
	}
	if Alive(socketPath) {
		return nil, fmt.Errorf("a broker is already listening on %s", socketPath)
	}
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("restrict socket: %w", err)
	}
	return ln, nil
}

// Serve runs h on ln until ctx is cancelled, then drains briefly.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			_ = srv.Close() // requests still running after the grace period are cut off
		}
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
