package lima

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
)

//go:embed template.yaml
var templateText string

var limaTemplate = template.Must(template.New("lima").Parse(templateText))

// MinDiskGiB keeps enough room for the Ubuntu image plus several microVM disks.
const MinDiskGiB = 10

// TemplateOptions size the Lima VM that hosts the microVMs.
type TemplateOptions struct {
	CPUs      int
	MemoryGiB int
	DiskGiB   int
}

// Validate checks the sizing before anything is created.
func (o TemplateOptions) Validate() error {
	if o.CPUs < 1 {
		return fmt.Errorf("cpus must be at least 1, got %d", o.CPUs)
	}
	if o.MemoryGiB < 1 {
		return fmt.Errorf("memory must be at least 1 GiB, got %d", o.MemoryGiB)
	}
	if o.DiskGiB < MinDiskGiB {
		return fmt.Errorf("disk must be at least %d GiB, got %d", MinDiskGiB, o.DiskGiB)
	}
	return nil
}

// RenderTemplate produces the lima.yaml used to create the instance.
func RenderTemplate(o TemplateOptions) (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := limaTemplate.Execute(&buf, o); err != nil {
		return "", fmt.Errorf("render lima template: %w", err)
	}
	return buf.String(), nil
}
