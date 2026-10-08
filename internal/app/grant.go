package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
	"github.com/itlabs-gmbh/cracklet/internal/config"
	"github.com/itlabs-gmbh/cracklet/internal/grant"
	"github.com/itlabs-gmbh/cracklet/internal/guest"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

func (a *App) grantStore() grant.Store {
	return grant.Store{Dir: a.paths.VMsDir()}
}

// loadCaps resolves embedded and user capabilities.
func (a *App) loadCaps() (cap.Set, error) {
	return cap.Load(a.paths.CapsDir())
}

// Grant allows capabilities for a VM and provisions the guest accordingly.
func (a *App) Grant(ctx context.Context, name string, specs []string) (grant.Set, error) {
	if err := vm.ValidateName(name); err != nil {
		return nil, err
	}
	caps, err := a.loadCaps()
	if err != nil {
		return nil, err
	}
	requested, err := grant.ParseSet(specs)
	if err != nil {
		return nil, err
	}
	for _, g := range requested {
		if err := checkGrant(caps, g); err != nil {
			return nil, err
		}
	}
	store := a.grantStore()
	unlock, err := store.Lock(name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	set, err := store.Load(name)
	if err != nil {
		return nil, err
	}
	for _, g := range requested {
		set = set.Add(g)
	}
	// Configure the guest first: a grant is only persisted once the guest can
	// actually use it, so a failed provision never leaves a silent allow.
	if err := a.provision(ctx, name, caps, set); err != nil {
		return nil, err
	}
	if err := store.Save(name, set); err != nil {
		return nil, err
	}
	a.printf("%s may now use: %s\n", name, strings.Join(set.Strings(), ", "))
	return set, nil
}

// Revoke withdraws capabilities and removes their guest configuration.
func (a *App) Revoke(ctx context.Context, name string, specs []string) (grant.Set, error) {
	if err := vm.ValidateName(name); err != nil {
		return nil, err
	}
	requested, err := grant.ParseSet(specs)
	if err != nil {
		return nil, err
	}
	store := a.grantStore()
	unlock, err := store.Lock(name)
	if err != nil {
		return nil, err
	}
	defer unlock()
	set, err := store.Load(name)
	if err != nil {
		return nil, err
	}
	for _, g := range requested {
		if !set.Contains(g) {
			return nil, fmt.Errorf("%s is not granted to %s", g, name)
		}
		set = set.Remove(g)
	}
	if err := store.Save(name, set); err != nil {
		return nil, err
	}
	caps, err := a.loadCaps()
	if err != nil {
		return nil, err
	}
	if err := a.provision(ctx, name, caps, set); err != nil {
		return nil, err
	}
	if len(set) == 0 {
		a.printf("%s has no grants left\n", name)
	} else {
		a.printf("%s may still use: %s\n", name, strings.Join(set.Strings(), ", "))
	}
	return set, nil
}

// Grants returns the current grants of a VM.
func (a *App) Grants(name string) (grant.Set, error) {
	if err := vm.ValidateName(name); err != nil {
		return nil, err
	}
	return a.grantStore().Load(name)
}

// checkGrant verifies that a grant names a known capability and that a scope
// is only given where the capability is scoped.
func checkGrant(caps cap.Set, g grant.Grant) error {
	if g.Cap == grant.SSHAgent {
		if g.Scope != "" {
			return fmt.Errorf("%s takes no scope", grant.SSHAgent)
		}
		return nil
	}
	c, ok := caps[g.Cap]
	if !ok {
		return fmt.Errorf("unknown capability %q (see 'cracklet cap ls')", g.Cap)
	}
	scoped := c.Proxy != nil && c.Proxy.ScopeSegments > 0
	switch {
	case scoped && g.Scope == "":
		return fmt.Errorf("%s needs a scope, e.g. %s:org/repo or %s:*", g.Cap, g.Cap, g.Cap)
	case !scoped && g.Scope != "":
		return fmt.Errorf("%s takes no scope", g.Cap)
	}
	return nil
}

// templateData is what guest templates of a VM see.
func (a *App) templateData(name string) (cap.TemplateData, error) {
	token, err := a.grantStore().Token(name)
	if err != nil {
		return cap.TemplateData{}, err
	}
	return cap.TemplateData{
		VM:           name,
		BrokerURL:    fmt.Sprintf("http://127.0.0.1:%d", config.BrokerGuestPort),
		PseudoToken:  token,
		BrokerSocket: config.BrokerGuestSocket,
	}, nil
}

// provision renders the guest configuration for the granted caps and applies
// it over SSH: one call reads the current state, one call applies the plan.
func (a *App) provision(ctx context.Context, name string, caps cap.Set, set grant.Set) error {
	data, err := a.templateData(name)
	if err != nil {
		return err
	}
	var granted []cap.Cap
	for _, n := range set.Caps() {
		if c, ok := caps[n]; ok {
			granted = append(granted, c)
		}
	}
	plan, err := guest.Render(granted, data)
	if err != nil {
		return err
	}
	if err := a.requireRunning(ctx); err != nil {
		return err
	}
	if err := a.ensureSSHConfig(); err != nil {
		return err
	}
	out, err := a.sshScript(ctx, name, plan.ReadScript())
	if err != nil {
		return fmt.Errorf("read guest state of %s: %w", name, err)
	}
	state, err := guest.ParseState(out)
	if err != nil {
		return err
	}
	script, err := plan.ApplyScript(state)
	if err != nil {
		return err
	}
	if _, err := a.sshScript(ctx, name, script); err != nil {
		return fmt.Errorf("apply guest configuration to %s: %w", name, err)
	}
	return nil
}

// sshScript runs a shell script in the guest. The script is streamed to
// `bash -s` over stdin, so neither the remote shell's quoting rules nor the
// argument length limit apply, however large a merged JSON file gets.
func (a *App) sshScript(ctx context.Context, name, script string) ([]byte, error) {
	return a.r.OutputWithInput(ctx, strings.NewReader(script), "ssh", a.sshArgs(name, []string{"bash", "-s"})...)
}
