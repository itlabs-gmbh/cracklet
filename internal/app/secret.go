package app

import (
	"context"
	"fmt"
	"regexp"
	"sort"

	"github.com/itlabs-gmbh/cracklet/internal/secret"
)

var secretNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// SecretSet stores a value in the macOS Keychain for caps to reference as
// keychain:cracklet/<name>.
func (a *App) SecretSet(ctx context.Context, name, value string) error {
	if !secretNameRe.MatchString(name) {
		return fmt.Errorf("secret name %q must be lowercase letters, digits and dashes", name)
	}
	if err := secret.Store(ctx, a.r, name, value); err != nil {
		return err
	}
	a.printf("stored keychain:%s/%s\n", secret.KeychainService, name)
	return nil
}

// SecretRemove deletes a stored value.
func (a *App) SecretRemove(ctx context.Context, name string) error {
	if !secretNameRe.MatchString(name) {
		return fmt.Errorf("secret name %q must be lowercase letters, digits and dashes", name)
	}
	if err := secret.Delete(ctx, a.r, name); err != nil {
		return err
	}
	a.printf("removed keychain:%s/%s\n", secret.KeychainService, name)
	return nil
}

func sortStrings(s []string) { sort.Strings(s) }
