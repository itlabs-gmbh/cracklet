package host

import (
	"testing"
)

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

func TestHasFatal(t *testing.T) {
	if HasFatal([]Problem{{Code: "a"}, {Code: "b"}}) {
		t.Error("no fatal problem, HasFatal must be false")
	}
	if !HasFatal([]Problem{{Code: "a"}, {Code: "b", Fatal: true}}) {
		t.Error("a fatal problem must be reported")
	}
}
