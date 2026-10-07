package broker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClaimIsExclusive(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "cracklet-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "run", "vm1.sock")
	release, err := Claim(socket)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := Claim(socket); !errors.Is(err, ErrBusy) {
		t.Fatalf("second claim must report ErrBusy, got %v", err)
	}
	release()
	again, err := Claim(socket)
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	again()
}
