package broker

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// inheritedEnv is the only part of the Mac's environment child processes
// see; everything else could be read back by a guest-driven tool.
var inheritedEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TMPDIR", "TERM"}

// childEnv builds a minimal environment plus the declared extras.
func childEnv(extra map[string]string) []string {
	env := make([]string, 0, len(inheritedEnv)+len(extra))
	for _, k := range inheritedEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// isolateChild puts the child in its own process group so that killing it
// also reaches grandchildren such as the servers npx spawns, and bounds how
// long a finished command may hold its pipes before they are forced closed.
// Commands created with a context additionally get the group kill as their
// cancel action (exec rejects Cancel on context-free commands).
func isolateChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 3 * time.Second
}

// cancelGroup makes context cancellation kill the whole process group.
func cancelGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return killGroup(cmd) }
}

// killGroup terminates the child's whole process group.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
