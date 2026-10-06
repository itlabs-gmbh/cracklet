package host

import (
	"strings"
	"testing"
)

func TestChipGeneration(t *testing.T) {
	cases := []struct {
		brand string
		want  int
		ok    bool
	}{
		{"Apple M1", 1, true},
		{"Apple M2 Max", 2, true},
		{"Apple M3 Pro", 3, true},
		{"Apple M4 Pro", 4, true},
		{"Apple M5", 5, true},
		{"Intel(R) Core(TM) i9-9980HK", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := ChipGeneration(c.brand)
		if got != c.want || ok != c.ok {
			t.Errorf("ChipGeneration(%q) = (%d, %v), want (%d, %v)", c.brand, got, ok, c.want, c.ok)
		}
	}
}

func TestMajorVersion(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"26.7", 26, false},
		{"15.1.1", 15, false},
		{"15", 15, false},
		{"", 0, true},
		{"abc", 0, true},
	}
	for _, c := range cases {
		got, err := MajorVersion(c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("MajorVersion(%q) = (%d, %v), want (%d, err=%v)", c.in, got, err, c.want, c.wantErr)
		}
	}
}

func goodFacts() Facts {
	return Facts{
		OS:           "darwin",
		Arch:         "arm64",
		MacOSVersion: "26.7",
		Chip:         "Apple M4 Pro",
		HVSupport:    true,
		HasBrew:      true,
		HasLima:      true,
	}
}

func TestCheckPasses(t *testing.T) {
	if problems := Check(goodFacts()); len(problems) != 0 {
		t.Fatalf("expected no problems, got %+v", problems)
	}
}

func TestCheckReportsEachProblem(t *testing.T) {
	f := goodFacts()
	f.MacOSVersion = "14.5"
	f.Chip = "Apple M2"
	f.HVSupport = false
	f.HasLima = false
	f.HasBrew = false

	codes := map[string]bool{}
	for _, p := range Check(f) {
		codes[p.Code] = true
	}
	for _, want := range []string{"macos-version", "chip", "hv-support", "lima-missing"} {
		if !codes[want] {
			t.Errorf("missing problem code %q in %v", want, codes)
		}
	}
}

func TestCheckLimaMissingIsFixableWithBrew(t *testing.T) {
	f := goodFacts()
	f.HasLima = false
	problems := Check(f)
	if len(problems) != 1 || problems[0].Code != "lima-missing" || problems[0].Fatal {
		t.Fatalf("expected a single non-fatal lima-missing problem, got %+v", problems)
	}
	if !strings.Contains(problems[0].Message, "brew") {
		t.Errorf("message should mention brew: %q", problems[0].Message)
	}
}

func TestCheckRejectsWrongPlatform(t *testing.T) {
	f := goodFacts()
	f.OS = "linux"
	f.Arch = "amd64"
	problems := Check(f)
	if len(problems) == 0 || problems[0].Code != "platform" || !problems[0].Fatal {
		t.Fatalf("expected fatal platform problem, got %+v", problems)
	}
}
