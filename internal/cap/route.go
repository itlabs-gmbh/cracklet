package cap

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Route forwards requests that reach the broker for Host instead of under
// /<cap>/ to a second upstream of the same capability. It serves clients that
// can be pointed at a Unix socket but not at a base URL, such as gh with
// http_unix_socket: they keep their real host name and path, and the broker
// picks the route by the Host header. strip_headers and allow_headers of the
// proxy apply to routes as well.
type Route struct {
	// Host is the host name the client sends, without a port.
	Host string `toml:"host"`
	// Upstream is the base URL requests for Host are forwarded to.
	Upstream string `toml:"upstream"`
	// Headers are set on every upstream request, like proxy.headers.
	Headers map[string]string `toml:"headers"`
	// ScopePrefix is the path segment the grant scope follows, such as repos
	// for /repos/org/repo/...; the scope is then the next scope_segments
	// segments. Requests outside the prefix are not tied to one scope and
	// need the wildcard grant.
	ScopePrefix string `toml:"scope_prefix"`
	// WildcardSubpaths are segments right after the scope whose endpoints act
	// beyond it (GitHub's /repos/o/r/transfer moves the repo elsewhere); they
	// need the wildcard grant as well.
	WildcardSubpaths []string `toml:"wildcard_subpaths"`
}

var (
	hostRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	segmentRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// routableHost accepts public DNS names only. The guest reaches the broker as
// 127.0.0.1, so a route for a loopback, IP or single-label name would catch
// the requests of every other capability, granted or not.
func routableHost(host string) bool {
	if !hostRe.MatchString(host) || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	return host != "localhost" && !strings.HasSuffix(host, ".localhost")
}

func (p Proxy) validateRoutes() []string {
	var problems []string
	seen := map[string]bool{}
	for i, r := range p.Routes {
		at := fmt.Sprintf("proxy.routes[%d]", i)
		if !routableHost(r.Host) {
			problems = append(problems, at+".host must be a lowercase DNS name with a dot, without port; IPs and localhost are not allowed")
		} else if seen[r.Host] {
			problems = append(problems, fmt.Sprintf("%s: host %s is routed twice", at, r.Host))
		}
		seen[r.Host] = true
		if err := validateUpstream(r.Upstream); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", at, err))
		}
		if r.ScopePrefix != "" {
			if !segmentRe.MatchString(r.ScopePrefix) || r.ScopePrefix == "." || r.ScopePrefix == ".." {
				problems = append(problems, at+".scope_prefix must be a single path segment")
			}
			if p.ScopeSegments == 0 {
				problems = append(problems, at+".scope_prefix needs scope_segments on the proxy")
			}
		}
		for _, seg := range r.WildcardSubpaths {
			if !segmentRe.MatchString(seg) || seg == "." || seg == ".." {
				problems = append(problems, fmt.Sprintf("%s.wildcard_subpaths: %q must be a single path segment", at, seg))
			}
		}
		problems = append(problems, validateHeaders(at+".headers", r.Headers)...)
	}
	return problems
}

// validateHeaders checks broker-side header templates.
func validateHeaders(at string, headers map[string]string) []string {
	var problems []string
	for _, k := range sortedKeys(headers) {
		if err := checkTemplate(headers[k], true); err != nil {
			problems = append(problems, fmt.Sprintf("%s.%s: %v", at, k, err))
			continue
		}
		if _, err := SecretRefs(headers[k]); err != nil {
			problems = append(problems, fmt.Sprintf("%s.%s: %v", at, k, err))
		}
	}
	return problems
}

// SecretRefs lists every secret the proxy and its routes send upstream, in
// header order. The templates were validated, so the list is exact.
func (p Proxy) SecretRefs() []string {
	var refs []string
	add := func(headers map[string]string) {
		for _, k := range sortedKeys(headers) {
			r, _ := SecretRefs(headers[k])
			refs = append(refs, r...)
		}
	}
	add(p.Headers)
	for _, r := range p.Routes {
		add(r.Headers)
	}
	return refs
}

// RouteFor finds the capability whose proxy routes host.
func (s Set) RouteFor(host string) (Cap, Route, bool) {
	for _, name := range s.Names() {
		c := s[name]
		if c.Proxy == nil {
			continue
		}
		for _, r := range c.Proxy.Routes {
			if r.Host == host {
				return c, r, true
			}
		}
	}
	return Cap{}, Route{}, false
}

// Validate checks rules that span capabilities: a host may be routed by one
// capability only, otherwise a grant for one would decide over the other.
func (s Set) Validate() error {
	owner := map[string]string{}
	var problems []string
	for _, name := range s.Names() {
		c := s[name]
		if c.Proxy == nil {
			continue
		}
		for _, r := range c.Proxy.Routes {
			if other, dup := owner[r.Host]; dup {
				problems = append(problems, fmt.Sprintf("caps %s (%s) and %s (%s) both route %s; remove or edit one of them",
					other, s[other].Source, name, c.Source, r.Host))
				continue
			}
			owner[r.Host] = name
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}
