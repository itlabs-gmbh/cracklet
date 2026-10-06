package envdbin

import "testing"

// The daemon is gitignored, so Embedded must tolerate both states: present after
// `make envd`, absent after a bare `go build` or `go install ...@latest`.
func TestEmbeddedIsConsistentWithFS(t *testing.T) {
	data, ok := Embedded()
	_, statErr := files.ReadFile(embeddedPath)
	if ok != (statErr == nil) {
		t.Fatalf("Embedded ok=%v but ReadFile err=%v", ok, statErr)
	}
	if ok && len(data) < MinSize {
		t.Errorf("embedded daemon is suspiciously small: %d bytes", len(data))
	}
	if !ok && data != nil {
		t.Error("absent daemon must yield nil data")
	}
}
