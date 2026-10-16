package vm

import "testing"

func TestValidateName(t *testing.T) {
	valid := []string{"vm1", "my-vm", "a", "dev-box-2"}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) unexpected error: %v", n, err)
		}
	}
	invalid := []string{"", "VM", "-x", "a_b", "x.y", "1abc", "this-name-is-way-too-long-for-a-vm-name"}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) expected error", n)
		}
	}
}

func TestValidateSpec(t *testing.T) {
	good := Spec{Name: "vm1", VCPUs: 2, MemMiB: 1024}
	if err := good.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bad := []Spec{
		{Name: "vm1", VCPUs: 0, MemMiB: 1024},
		{Name: "vm1", VCPUs: 2, MemMiB: 512}, // Apple's container needs 1 GiB
		{Name: "vm1", VCPUs: 2, MemMiB: MaxMemMiB + 1},
		{Name: "Bad", VCPUs: 2, MemMiB: 1024},
		{Name: "vm1", VCPUs: 2, MemMiB: 1024, Forwards: []string{"80"}},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("Validate(%+v) expected error", s)
		}
	}
}

func TestValidateSpecAllowsEmptyName(t *testing.T) {
	s := Spec{VCPUs: 1, MemMiB: 1024}
	if err := s.Validate(); err != nil {
		t.Fatalf("empty name means auto-generated; got error %v", err)
	}
}

func TestValidateLabel(t *testing.T) {
	for _, l := range []string{"paseo-skiff", "3", "paseo.3", "Task_7", "a"} {
		if err := ValidateLabel("owner", l); err != nil {
			t.Errorf("ValidateLabel(%q) unexpected error: %v", l, err)
		}
	}
	tooLong := "a123456789012345678901234567890123456789012345678901234567890123"
	for _, l := range []string{"", "-", "-x", ".x", "a b", "a;b", "a/b", "a:b", "a$b", "ä", tooLong} {
		if err := ValidateLabel("owner", l); err == nil {
			t.Errorf("ValidateLabel(%q) expected error", l)
		}
	}
}

func TestValidateSpecMetadata(t *testing.T) {
	base := Spec{Name: "vm1", VCPUs: 2, MemMiB: 1024}
	good := []Spec{base, {Name: "vm1", VCPUs: 2, MemMiB: 1024, Owner: "paseo"}, {Name: "vm1", VCPUs: 2, MemMiB: 1024, Owner: "paseo", Slot: "3"}}
	for _, s := range good {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v) unexpected error: %v", s, err)
		}
	}
	bad := []Spec{
		{Name: "vm1", VCPUs: 2, MemMiB: 1024, Owner: "a b"},
		{Name: "vm1", VCPUs: 2, MemMiB: 1024, Owner: "paseo", Slot: "-"},
		{Name: "vm1", VCPUs: 2, MemMiB: 1024, Slot: "3"}, // a slot belongs to an owner's pool
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("Validate(%+v) expected error", s)
		}
	}
}
