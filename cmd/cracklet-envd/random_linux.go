//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	randomDev = "/dev/urandom"
	// randPoolHeader is the size of struct rand_pool_info's two int fields
	// (entropy_count, buf_size) that precede the seed bytes.
	randPoolHeader = 8
	bitsPerByte    = 8
)

// SeedRandom hands the seed to the kernel with RNDADDENTROPY, which mixes it
// into the input pool *and* credits it as entropy, then issues RNDRESEEDCRNG
// so the CRNG picks it up immediately instead of at the next scheduled
// reseed. Both ioctls require CAP_SYS_ADMIN, which envd has as root.
func (linuxSystem) SeedRandom(seed []byte) error {
	if len(seed) == 0 {
		return fmt.Errorf("empty seed")
	}
	f, err := os.OpenFile(randomDev, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	// struct rand_pool_info { int entropy_count; int buf_size; __u32 buf[0]; }
	// Pad the payload to a multiple of 4 so buf_size describes whole __u32s.
	payload := (len(seed) + 3) &^ 3
	info := make([]byte, randPoolHeader+payload)
	binary.NativeEndian.PutUint32(info[0:4], uint32(len(seed)*bitsPerByte))
	binary.NativeEndian.PutUint32(info[4:8], uint32(payload))
	copy(info[randPoolHeader:], seed)

	if err := ioctl(f.Fd(), unix.RNDADDENTROPY, uintptr(unsafe.Pointer(&info[0]))); err != nil {
		return fmt.Errorf("RNDADDENTROPY: %w", err)
	}
	if err := ioctl(f.Fd(), unix.RNDRESEEDCRNG, 0); err != nil {
		return fmt.Errorf("RNDRESEEDCRNG: %w", err)
	}
	return nil
}

func ioctl(fd uintptr, req uint, arg uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(req), arg); errno != 0 {
		return errno
	}
	return nil
}
