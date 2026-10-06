//go:build linux

package main

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// vsockListener wraps a raw AF_VSOCK socket. Go's net package has no vsock
// support (net.FileConn rejects the address family), so connections are
// handled as plain file descriptors.
type vsockListener struct {
	fd int
}

func listen(port uint32) (*vsockListener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind: %w", err)
	}
	if err := unix.Listen(fd, 8); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("listen: %w", err)
	}
	return &vsockListener{fd: fd}, nil
}

// Accept blocks for the next connection and returns it as a file.
func (l *vsockListener) Accept() (io.ReadWriteCloser, error) {
	nfd, _, err := unix.Accept4(l.fd, unix.SOCK_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(nfd), "vsock-conn"), nil
}
