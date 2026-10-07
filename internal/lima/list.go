package lima

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Status mirrors the status strings reported by `limactl list`.
type Status string

const (
	StatusRunning Status = "Running"
	StatusStopped Status = "Stopped"
)

// Instance is the subset of `limactl list --format json` cracklet cares about.
type Instance struct {
	Name         string `json:"name"`
	Status       Status `json:"status"`
	Dir          string `json:"dir"`
	SSHLocalPort int    `json:"sshLocalPort"`
	CPUs         int    `json:"cpus"`
	Memory       int64  `json:"memory"` // bytes
	Disk         int64  `json:"disk"`   // bytes
}

// ParseList decodes limactl's JSON output, which is either newline-delimited
// objects (one per instance) or a JSON array.
func ParseList(data []byte) ([]Instance, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var list []Instance
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("parse limactl list output: %w", err)
		}
		return list, nil
	}
	var list []Instance
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	for {
		var inst Instance
		if err := dec.Decode(&inst); err == io.EOF {
			return list, nil
		} else if err != nil {
			return nil, fmt.Errorf("parse limactl list output: %w", err)
		}
		list = append(list, inst)
	}
}

// Find returns the instance with the given name.
func Find(list []Instance, name string) (Instance, bool) {
	for _, inst := range list {
		if inst.Name == name {
			return inst, true
		}
	}
	return Instance{}, false
}
