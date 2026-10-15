package backend

import (
	"os"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/config"
)

func TestSelectionDefaultsToLima(t *testing.T) {
	paths := config.Paths{Home: t.TempDir() + "/missing"}
	name, err := LoadName(paths)
	if err != nil || name != NameLima {
		t.Fatalf("LoadName = %q, %v; want lima without a stored choice", name, err)
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
