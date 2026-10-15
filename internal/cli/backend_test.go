package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/itlabs-gmbh/cracklet/internal/app"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// TestPrepareBackendFlagReachesTheBuilder: the backend choice must be known
// before the App exists, so prepare runs on the backend it selects.
func TestPrepareBackendFlagReachesTheBuilder(t *testing.T) {
	var chosen []string
	build := func(out io.Writer, name string) (*app.App, error) {
		chosen = append(chosen, name)
		fake := runner.NewFake(fakeHandler(t))
		return app.New(fake, config.Paths{Home: t.TempDir(), LimaHome: "/tmp/lima"}, out), nil
	}
	for _, args := range [][]string{{"prepare", "--backend", "container"}, {"ls"}} {
		root := newRoot(build)
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(args)
		_ = root.Execute() // prepare itself fails on the fake runner; the choice is what matters here
	}
	if len(chosen) != 2 || chosen[0] != "container" || chosen[1] != "" {
		t.Errorf("builder saw %q, want [container \"\"]", chosen)
	}
}

func TestDoctorNamesTheBackend(t *testing.T) {
	out, _, _ := run(t, "doctor")
	if !strings.Contains(out, "backend: lima") {
		t.Errorf("doctor must name the backend:\n%s", out)
	}
}
