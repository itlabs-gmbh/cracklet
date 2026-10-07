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

func TestParseSize(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"2G", 2 << 30, false},
		{"512M", 512 << 20, false},
		{"10g", 10 << 30, false},
		{"1T", 0, true},
		{"abc", 0, true},
		{"", 0, true},
		{"0G", 0, true},
		{"17179869186G", 0, true}, // would wrap around to 2 GiB
		{"9223372036854775807M", 0, true},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("ParseSize(%q) = (%d, %v), want (%d, err=%v)", c.in, got, err, c.want, c.wantErr)
		}
	}
}

func TestValidateSpec(t *testing.T) {
	good := Spec{Name: "vm1", VCPUs: 2, MemMiB: 1024, Disk: "2G"}
	if err := good.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bad := []Spec{
		{Name: "vm1", VCPUs: 0, MemMiB: 1024, Disk: "2G"},
		{Name: "vm1", VCPUs: 2, MemMiB: 64, Disk: "2G"},
		{Name: "vm1", VCPUs: 2, MemMiB: MaxMemMiB + 1, Disk: "2G"},
		{Name: "vm1", VCPUs: 2, MemMiB: 1024, Disk: "1G"},
		{Name: "vm1", VCPUs: 2, MemMiB: 1024, Disk: "100000G"},
		{Name: "Bad", VCPUs: 2, MemMiB: 1024, Disk: "2G"},
		{Name: "vm1", VCPUs: 2, MemMiB: 1024, Disk: "nope"},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("Validate(%+v) expected error", s)
		}
	}
}

func TestValidateSpecAllowsEmptyName(t *testing.T) {
	s := Spec{VCPUs: 1, MemMiB: 256, Disk: "2G"}
	if err := s.Validate(); err != nil {
		t.Fatalf("empty name means auto-generated; got error %v", err)
	}
}

func TestNormalizeSize(t *testing.T) {
	cases := map[string]string{"4g": "4G", " 2G\n": "2G", "512m": "512M", "10G": "10G"}
	for in, want := range cases {
		got, err := NormalizeSize(in)
		if err != nil || got != want {
			t.Errorf("NormalizeSize(%q) = (%q, %v), want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeSize("1T"); err == nil {
		t.Error("NormalizeSize must reject invalid sizes")
	}
}

func TestSpecMode(t *testing.T) {
	if (Spec{}).Mode() != "snapshot" || (Spec{Fresh: true}).Mode() != "fresh" {
		t.Error("Mode must map Fresh to fresh and default to snapshot")
	}
}

func TestProfiles(t *testing.T) {
	for name, want := range map[string]string{"": "2G", ProfileBase: "2G", ProfilePaseo: "8G"} {
		got, err := ProfileDisk(name)
		if err != nil || got != want {
			t.Errorf("ProfileDisk(%q) = (%q, %v), want %q", name, got, err, want)
		}
	}
	for _, bad := range []string{"Paseo", "node", "../x"} {
		if err := ValidateProfile(bad); err == nil {
			t.Errorf("ValidateProfile(%q) expected error", bad)
		}
	}
	if got := ProfileNames(); len(got) != 2 || got[0] != ProfileBase || got[1] != ProfilePaseo {
		t.Errorf("ProfileNames() = %v", got)
	}
}

func TestSpecDiskDefaultsToProfileImage(t *testing.T) {
	cases := []struct {
		spec Spec
		want string
	}{
		{Spec{}, "2G"},
		{Spec{Profile: ProfilePaseo}, "8G"},
		{Spec{Profile: ProfilePaseo, Disk: "12G"}, "12G"},
	}
	for _, c := range cases {
		if got := c.spec.DiskSize(); got != c.want {
			t.Errorf("DiskSize(%+v) = %q, want %q", c.spec, got, c.want)
		}
	}
	if got := (Spec{}).ProfileName(); got != ProfileBase {
		t.Errorf("empty profile must mean %q, got %q", ProfileBase, got)
	}
}

func TestValidateSpecProfile(t *testing.T) {
	if err := (Spec{VCPUs: 2, MemMiB: 1024, Profile: ProfilePaseo}).Validate(); err != nil {
		t.Fatalf("paseo with default disk rejected: %v", err)
	}
	bad := []Spec{
		{VCPUs: 2, MemMiB: 1024, Profile: "nope"},
		// the paseo image is 8G and disks only grow
		{VCPUs: 2, MemMiB: 1024, Profile: ProfilePaseo, Disk: "4G"},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("Validate(%+v) expected error", s)
		}
	}
}
