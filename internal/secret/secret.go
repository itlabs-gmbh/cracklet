// Package secret resolves credential references used by capabilities. The
// schemes are deliberately few and harness-neutral: keychain:, env:, cmd:
// and file:. Values are resolved on the Mac at request time and never leave it.
package secret

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/itlabs-gmbh/cracklet/internal/runner"
)

// KeychainService is the macOS Keychain service name cracklet stores secrets under.
const KeychainService = "cracklet"

// defaultTTL bounds how long a resolved value is reused before it is fetched
// again, so rotated secrets are picked up without a `security` call per request.
const defaultTTL = 60 * time.Second

// failureTTL is how long a failed lookup is remembered, so a burst of guest
// requests does not trigger a burst of Keychain prompts.
const failureTTL = 5 * time.Second

// Resolver turns references into values.
type Resolver struct {
	Runner    runner.Runner
	LookupEnv func(string) (string, bool)
	ReadFile  func(string) ([]byte, error)
	TTL       time.Duration
	Now       func() time.Time

	mu       sync.Mutex
	cache    map[string]cached
	inflight map[string]*sync.Mutex
}

type cached struct {
	value   string
	err     error
	expires time.Time
}

// NewResolver returns a Resolver wired to the real environment.
func NewResolver(r runner.Runner) *Resolver {
	return &Resolver{Runner: r, LookupEnv: os.LookupEnv, ReadFile: os.ReadFile, TTL: defaultTTL, Now: time.Now}
}

// ParseRef splits "scheme:rest" and validates the scheme.
func ParseRef(ref string) (scheme, rest string, err error) {
	scheme, rest, ok := strings.Cut(ref, ":")
	if !ok || rest == "" {
		return "", "", fmt.Errorf("secret reference %q must look like scheme:value", ref)
	}
	switch scheme {
	case "keychain", "env", "cmd", "file":
		return scheme, rest, nil
	}
	return "", "", fmt.Errorf("secret reference %q: unknown scheme %q (use keychain:, env:, cmd: or file:)", ref, scheme)
}

// Resolve returns the value behind ref, trimmed of a trailing newline.
// Concurrent calls for the same ref share one lookup.
func (r *Resolver) Resolve(ctx context.Context, ref string) (string, error) {
	lock := r.refLock(ref)
	lock.Lock()
	defer lock.Unlock()
	if c, ok := r.fromCache(ref); ok {
		return c.value, c.err
	}
	value, err := r.lookup(ctx, ref)
	r.store(ref, value, err)
	return value, err
}

func (r *Resolver) lookup(ctx context.Context, ref string) (string, error) {
	scheme, rest, err := ParseRef(ref)
	if err != nil {
		return "", err
	}
	var value string
	switch scheme {
	case "keychain":
		value, err = r.keychain(ctx, rest)
	case "env":
		value, err = r.env(rest)
	case "cmd":
		value, err = r.command(ctx, rest)
	case "file":
		value, err = r.file(rest)
	}
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	value = strings.TrimRight(value, "\r\n")
	if value == "" {
		return "", fmt.Errorf("resolve %s: empty value", ref)
	}
	return value, nil
}

func (r *Resolver) refLock(ref string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inflight == nil {
		r.inflight = map[string]*sync.Mutex{}
	}
	m, ok := r.inflight[ref]
	if !ok {
		m = &sync.Mutex{}
		r.inflight[ref] = m
	}
	return m
}

func (r *Resolver) keychain(ctx context.Context, rest string) (string, error) {
	service, account, ok := strings.Cut(rest, "/")
	if !ok || service == "" || account == "" {
		return "", fmt.Errorf("keychain reference must be keychain:<service>/<account>")
	}
	out, err := r.Runner.Output(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		return "", fmt.Errorf("keychain item %s/%s not found (store it with 'cracklet secret set %s'): %w", service, account, account, err)
	}
	return string(out), nil
}

func (r *Resolver) env(name string) (string, error) {
	v, ok := r.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return v, nil
}

func (r *Resolver) command(ctx context.Context, shell string) (string, error) {
	out, err := r.Runner.Output(ctx, "sh", "-c", shell)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (r *Resolver) file(path string) (string, error) {
	data, err := r.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (r *Resolver) fromCache(ref string) (cached, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cache[ref]
	if !ok || r.now().After(c.expires) {
		return cached{}, false
	}
	return c, true
}

func (r *Resolver) store(ref, value string, err error) {
	ttl := r.TTL
	if err != nil {
		ttl = min(failureTTL, r.TTL)
	}
	if ttl <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = map[string]cached{}
	}
	r.cache[ref] = cached{value: value, err: err, expires: r.now().Add(ttl)}
}

func (r *Resolver) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// Store writes a value into the macOS Keychain under KeychainService/account,
// replacing an existing item. The whole command goes to `security -i` on
// stdin: the value never appears in argv, and unlike `-w` without an
// argument, security does not fall back to prompting on the terminal (which
// silently stored whatever the user typed at that prompt, usually nothing).
func Store(ctx context.Context, r runner.Runner, account, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("refusing to store an empty secret")
	}
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("refusing to store a multi-line secret")
	}
	if !accountRe.MatchString(account) {
		return fmt.Errorf("secret name %q must be lowercase letters, digits and dashes", account)
	}
	command := "add-generic-password -U -s " + KeychainService + " -a " + account + " -w " + securityQuote(value) + "\n"
	if err := r.RunWithInput(ctx, strings.NewReader(command), "security", "-i"); err != nil {
		return fmt.Errorf("store secret %s: %w", account, err)
	}
	return nil
}

var accountRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// securityQuote quotes a word for security's interactive command parser,
// which understands double quotes with backslash escapes.
func securityQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Delete removes a Keychain item written by Store.
func Delete(ctx context.Context, r runner.Runner, account string) error {
	if _, err := r.Output(ctx, "security", "delete-generic-password", "-s", KeychainService, "-a", account); err != nil {
		return fmt.Errorf("delete secret %s: %w", account, err)
	}
	return nil
}
