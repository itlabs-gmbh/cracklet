package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/itlabs-gmbh/cracklet/internal/cap"
	"github.com/itlabs-gmbh/cracklet/internal/grant"
	"github.com/itlabs-gmbh/cracklet/internal/guest"
	"github.com/itlabs-gmbh/cracklet/internal/vm"
)

// maxCapDownload bounds `cracklet cap add` downloads.
const maxCapDownload = 256 << 10

// CapList prints every known capability with its kind and origin.
func (a *App) CapList() error {
	caps, err := a.loadCaps()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSCOPED\tSOURCE\tDESCRIPTION")
	for _, name := range caps.Names() {
		c := caps[name]
		scoped := "no"
		if c.Proxy != nil && c.Proxy.ScopeSegments > 0 {
			scoped = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Name, c.Kind(), scoped, c.Source, c.Description)
	}
	fmt.Fprintf(w, "%s\tbuilt-in\tno\t-\tforward the Mac's SSH agent (ssh -A)\n", grant.SSHAgent)
	return w.Flush()
}

// CapShow prints a capability's source file. With render set, it prints what
// the guest of vmName would receive instead.
func (a *App) CapShow(name string, render bool, vmName string) error {
	if err := cap.ValidateName(name); err != nil {
		return err
	}
	caps, err := a.loadCaps()
	if err != nil {
		return err
	}
	c, ok := caps[name]
	if !ok {
		return fmt.Errorf("unknown capability %q (see 'cracklet cap ls')", name)
	}
	if !render {
		return a.printCapSource(c)
	}
	if vmName == "" {
		vmName = "example"
	} else if err := vm.ValidateName(vmName); err != nil {
		return err
	}
	data, err := a.templateData(vmName)
	if err != nil {
		return err
	}
	plan, err := guest.Render([]cap.Cap{c}, data)
	if err != nil {
		return err
	}
	a.printf("# guest configuration of %s for %s\n", name, vmName)
	for _, k := range sortedKeys(plan.Env) {
		a.printf("env %s=%s\n", k, plan.Env[k])
	}
	for _, f := range plan.Files {
		a.printf("file %s (%s):\n%s", f.Path, f.Mode, indent(f.Content))
	}
	for _, blk := range plan.Blocks {
		a.printf("block %s (section cracklet:%s):\n%s", blk.Path, blk.Owner, indent(blk.Content))
	}
	for _, m := range plan.Merges {
		a.printf("json %s %s = %v\n", m.Path, m.Key, m.Value)
	}
	return nil
}

func (a *App) printCapSource(c cap.Cap) error {
	var text []byte
	var err error
	if c.Source == cap.SourceEmbedded {
		text, err = cap.EmbeddedSource(c.Name)
	} else {
		text, err = os.ReadFile(c.Source)
	}
	if err != nil {
		return err
	}
	a.printf("# source: %s\n%s", c.Source, text)
	return nil
}

// CapLint validates one capability file or every user file.
func (a *App) CapLint(name string) error {
	if name != "" {
		if err := cap.ValidateName(name); err != nil {
			return err
		}
		if _, err := cap.ParseFile(filepath.Join(a.paths.CapsDir(), name+".toml")); err != nil {
			return err
		}
		a.printf("%s: ok\n", name)
		return nil
	}
	caps, err := a.loadCaps()
	if err != nil {
		return err
	}
	n := 0
	for _, c := range caps {
		if c.Source != cap.SourceEmbedded {
			n++
		}
	}
	a.printf("%d user capabilities in %s: ok\n", n, a.paths.CapsDir())
	return nil
}

// CapInit writes a commented skeleton for a new capability.
func (a *App) CapInit(name string) error {
	text, err := cap.Skeleton(name)
	if err != nil {
		return err
	}
	path := filepath.Join(a.paths.CapsDir(), name+".toml")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(a.paths.CapsDir(), 0o700); err != nil {
		return fmt.Errorf("create caps directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	a.printf("wrote %s\n  edit it, then: cracklet cap lint %s\n", path, name)
	return nil
}

// CapAdd downloads a capability file, shows it, and installs it after
// confirmation (or straight away with yes).
func (a *App) CapAdd(ctx context.Context, url string, yes bool, confirm io.Reader) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("capability URLs must use https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := *http.DefaultClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("redirect to non-https URL %s refused", req.URL)
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCapDownload+1))
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	if len(body) > maxCapDownload {
		return fmt.Errorf("%s is larger than %d bytes", url, maxCapDownload)
	}
	c, err := cap.Parse(string(body), url)
	if err != nil {
		return err
	}
	// A downloaded file must not silently take over an embedded capability:
	// a remote "claude.toml" could point the broker, and with it the stored
	// token, at any upstream. Overriding embedded caps stays a manual step.
	embedded, err := cap.Embedded()
	if err != nil {
		return err
	}
	if _, shadows := embedded[c.Name]; shadows {
		return fmt.Errorf("%q is an embedded capability; downloaded files may not replace it (copy it into %s by hand if you really want that)", c.Name, a.paths.CapsDir())
	}
	// Control characters could hide lines from the review below.
	a.printf("%s\n", strings.ToValidUTF8(stripControl(string(body)), "?"))
	a.printf("%s", stripControl(capSummary(c)))
	path := filepath.Join(a.paths.CapsDir(), c.Name+".toml")
	if _, err := os.Stat(path); err == nil {
		a.printf("note: this replaces the existing %s\n", path)
	}
	if !yes {
		a.printf("install as %s? [y/N] ", path)
		line, _ := bufio.NewReader(confirm).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return fmt.Errorf("not installed")
		}
	}
	if err := os.MkdirAll(a.paths.CapsDir(), 0o700); err != nil {
		return fmt.Errorf("create caps directory: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	a.printf("installed %s\n  grant it with: cracklet grant <vm> %s\n", path, c.Name)
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// capSummary spells out what a capability would do with your secrets, so the
// confirmation is about consequences rather than TOML.
func capSummary(c cap.Cap) string {
	var b strings.Builder
	switch c.Kind() {
	case cap.KindProxy:
		fmt.Fprintf(&b, "=> %s proxies guest requests to %s\n", c.Name, c.Proxy.Upstream)
		for _, k := range sortedKeys(c.Proxy.Headers) {
			for _, ref := range secretRefs(c.Proxy.Headers[k]) {
				fmt.Fprintf(&b, "   and sends the secret %s in the %s header\n", ref, k)
			}
		}
	case cap.KindMCP:
		fmt.Fprintf(&b, "=> %s runs %q on this Mac\n", c.Name, strings.Join(append([]string{c.MCP.Command}, c.MCP.Args...), " "))
	case cap.KindExec:
		fmt.Fprintf(&b, "=> %s runs %q on this Mac for every request\n", c.Name, strings.Join(append([]string{c.Exec.Command}, c.Exec.Args...), " "))
	}
	return b.String()
}

// secretRefs lists the secrets a header template sends. The template was
// validated by cap.Parse, which only admits literal references, so the list
// is exact: nothing a VM name or any other data could change at runtime.
func secretRefs(tmpl string) []string {
	refs, _ := cap.SecretRefs(tmpl)
	return refs
}

// stripControl replaces everything that could hide or reorder text in a
// terminal: C0 and C1 control characters (except newline and tab) and
// Unicode format characters such as bidi overrides and zero-width joiners.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '?'
		}
		return r
	}, s)
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}
