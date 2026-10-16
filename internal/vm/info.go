package vm

import "time"

// States a backend reports for a VM.
const (
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateCreating = "creating" // while `skiff new` still builds it
	StateBroken   = "broken"   // metadata missing, remove with `skiff rm`
)

// Info is what a backend reports about a VM.
type Info struct {
	Name     string    `json:"name"`
	Index    int       `json:"index"`
	IP       string    `json:"ip"`
	State    string    `json:"state"`
	VCPUs    int       `json:"vcpus"`
	MemMiB   int       `json:"mem_mib"`
	Forwards []Forward `json:"forwards"`
	// Grants are the host-side grants of the VM (not reported by the backend).
	Grants []string `json:"grants,omitempty"`
	// Owner and Slot label VMs managed by a tool; empty for VMs made by hand.
	Owner string `json:"owner"`
	Slot  string `json:"slot"`
	// CreatedAt is nil for VMs created before skiff recorded it.
	CreatedAt *time.Time `json:"created_at"`
}
