package vm

import "testing"

func TestParseForward(t *testing.T) {
	good := map[string]Forward{
		"8080":        {Host: 8080, Guest: 8080},
		"3000:80":     {Host: 3000, Guest: 80},
		" 1024:1 ":    {Host: 1024, Guest: 1},
		"65535:65535": {Host: 65535, Guest: 65535},
	}
	for in, want := range good {
		got, err := ParseForward(in)
		if err != nil || got != want {
			t.Errorf("ParseForward(%q) = (%+v, %v), want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "80", "80:80", "70000", "8080:0", "8080:70000", "a:b", "8080:", ":80", "1.5"} {
		if _, err := ParseForward(bad); err == nil {
			t.Errorf("ParseForward(%q) expected error", bad)
		}
	}
}

func TestForwardRendering(t *testing.T) {
	f := Forward{Host: 3000, Guest: 80}
	if f.Spec() != "3000:80" || f.Label() != "3000→80" {
		t.Errorf("unexpected rendering: %q %q", f.Spec(), f.Label())
	}
}

func TestSpecValidatesForwards(t *testing.T) {
	ok := Spec{Name: "web", VCPUs: 1, MemMiB: 256, Disk: "2G", Forwards: []string{"8080:80", "3000"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bad := Spec{Name: "web", VCPUs: 1, MemMiB: 256, Disk: "2G", Forwards: []string{"80"}}
	if err := bad.Validate(); err == nil {
		t.Fatal("privileged host port must be rejected")
	}
}
