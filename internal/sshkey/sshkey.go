// Package sshkey manages the key pair cracklet injects into every microVM.
package sshkey

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// Ensure generates the ed25519 key pair in paths.Home unless it exists.
func Ensure(ctx context.Context, r runner.Runner, paths config.Paths, out io.Writer) error {
	_, err := os.Stat(paths.KeyPath())
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check ssh key: %w", err)
	}
	if err := os.MkdirAll(paths.Home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", paths.Home, err)
	}
	fmt.Fprintf(out, "==> Generating SSH key %s\n", paths.KeyPath())
	if err := r.Run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "cracklet", "-f", paths.KeyPath()); err != nil {
		return fmt.Errorf("generate ssh key: %w", err)
	}
	return nil
}
