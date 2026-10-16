package backend

import (
	"os"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/skiff/internal/config"
)

func TestSelectionDefaultsToContainer(t *testing.T) {
	paths := config.Paths{Home: t.TempDir() + "/missing"}
	name, err := LoadName(paths)
	if err != nil || name != NameContainer {
		t.Fatalf("LoadName = %q, %v; want container without a stored choice", name, err)
	}
}

func TestSelectionRoundTripAndValidation(t *testing.T) {
	paths := config.Paths{Home: t.TempDir() + "/home"}
	if err := SaveName(paths, "docker"); err == nil || !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("SaveName must validate, got %v", err)
	}
	if err := SaveName(paths, NameContainer); err != nil {
		t.Fatal(err)
	}
	name, err := LoadName(paths)
	if err != nil || name != NameContainer {
		t.Fatalf("LoadName = %q, %v", name, err)
	}
	if err := os.WriteFile(paths.BackendPath(), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadName(paths); err == nil {
		t.Error("a corrupt backend file must be reported, not silently defaulted")
	}
}

func TestSaveNameConcurrently(t *testing.T) {
	paths := config.Paths{Home: t.TempDir()}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			name := NameLima
			if i%2 == 0 {
				name = NameContainer
			}
			done <- SaveName(paths, name)
		}(i)
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent SaveName: %v", err)
		}
	}
	if _, err := LoadName(paths); err != nil {
		t.Errorf("LoadName after concurrent saves: %v", err)
	}
	entries, _ := os.ReadDir(paths.Home)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}
