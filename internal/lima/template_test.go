package lima

import (
	"strings"
	"testing"
)

func TestRenderTemplate(t *testing.T) {
	out, err := RenderTemplate(TemplateOptions{CPUs: 4, MemoryGiB: 8, DiskGiB: 40})
	if err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	for _, want := range []string{
		"nestedVirtualization: true",
		"vmType: vz",
		"cpus: 4",
		`memory: "8GiB"`,
		`disk: "40GiB"`,
		"template:_images/ubuntu-24.04",
		"mounts: []",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered template missing %q:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"_default/mounts", "provision:", "ubuntu-lts"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("rendered template must not contain %q", forbidden)
		}
	}
}

func TestRenderTemplateRejectsInvalidOptions(t *testing.T) {
	for _, o := range []TemplateOptions{
		{CPUs: 0, MemoryGiB: 8, DiskGiB: 40},
		{CPUs: 2, MemoryGiB: 0, DiskGiB: 40},
		{CPUs: 2, MemoryGiB: 8, DiskGiB: 5},
	} {
		if _, err := RenderTemplate(o); err == nil {
			t.Errorf("RenderTemplate(%+v) expected error", o)
		}
	}
}
