//go:build !linux

package main

import (
	"errors"
	"io"
)

type vsockListener struct{}

func listen(uint32) (*vsockListener, error) {
	return nil, errors.New("vsock is only available on Linux guests")
}

func (*vsockListener) Accept() (io.ReadWriteCloser, error) {
	return nil, errors.New("vsock is only available on Linux guests")
}
