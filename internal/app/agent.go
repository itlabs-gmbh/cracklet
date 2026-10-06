package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/agent"
	"github.com/itlabs-gmbh/cracklet/internal/config"
)

// ensureAgent installs or refreshes the guest agent when its checksum differs.
func (a *App) ensureAgent(ctx context.Context) error {
	// The agent lives in a root-owned directory, so the probe needs sudo too.
	probe := "sha256sum " + config.AgentPath + " 2>/dev/null | cut -d' ' -f1"
	out, err := a.lima.ShellOutput(ctx, "sudo", "sh", "-c", probe)
	if err != nil {
		return fmt.Errorf("probe guest agent: %w", err)
	}
	if strings.TrimSpace(string(out)) == agent.Checksum() {
		return nil
	}
	if err := a.lima.WriteFile(ctx, strings.NewReader(agent.Script), config.AgentPath, "0755"); err != nil {
		return fmt.Errorf("install guest agent: %w", err)
	}
	return nil
}

// agentOutput runs an agent subcommand and returns its stdout (JSON).
func (a *App) agentOutput(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"sudo", config.AgentPath}, args...)
	out, err := a.lima.ShellOutput(ctx, full...)
	if err != nil {
		return nil, fmt.Errorf("agent %s: %w", args[0], err)
	}
	return out, nil
}

// agentRun runs a long-lived agent subcommand with the terminal attached.
func (a *App) agentRun(ctx context.Context, args ...string) error {
	full := append([]string{"sudo", config.AgentPath}, args...)
	if err := a.lima.Shell(ctx, full...); err != nil {
		return fmt.Errorf("agent %s: %w", args[0], err)
	}
	return nil
}

// readyForAgent checks the instance and refreshes the agent in one step.
func (a *App) readyForAgent(ctx context.Context) error {
	if err := a.requireRunning(ctx); err != nil {
		return err
	}
	return a.ensureAgent(ctx)
}
