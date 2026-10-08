package cap

import (
	"strings"
	"testing"
)

const routedProxy = `
name = "forge"
[proxy]
upstream = "https://forge.example"
[proxy.headers]
Authorization = "Basic {{ secret \"env:T\" | basicauth \"x\" }}"
[[proxy.routes]]
host = "api.forge.example"
upstream = "https://api.forge.example"
[proxy.routes.headers]
Authorization = "Bearer {{ secret \"env:API\" }}"
`

func TestParseProxyRoutes(t *testing.T) {
	c, err := Parse(routedProxy, "t")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.Proxy.Routes) != 1 {
		t.Fatalf("routes = %+v", c.Proxy.Routes)
	}
	r := c.Proxy.Routes[0]
	if r.Host != "api.forge.example" || r.Upstream != "https://api.forge.example" {
		t.Fatalf("route = %+v", r)
	}
	if !strings.Contains(r.Headers["Authorization"], `secret "env:API"`) {
		t.Fatalf("route headers = %v", r.Headers)
	}
}

func TestParseRejectsBadRoutes(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"missing host":      {`host = "api.forge.example"`, `host = ""`, "proxy.routes[0].host"},
		"host with port":    {`host = "api.forge.example"`, `host = "api.forge.example:443"`, "proxy.routes[0].host"},
		"uppercase host":    {`host = "api.forge.example"`, `host = "API.forge.example"`, "proxy.routes[0].host"},
		"cleartext":         {`upstream = "https://api.forge.example"`, `upstream = "http://api.forge.example"`, "cleartext"},
		"dynamic secret":    {`secret \"env:API\"`, `secret (print \"env:\" .VM)`, "literal reference"},
		"unknown route key": {`host = "api.forge.example"`, "host = \"api.forge.example\"\nbogus = 1", "unknown keys"},
		// Scopes are gone; a file that still declares one must not load as
		// if the broker enforced it.
		"route scope":       {`host = "api.forge.example"`, "host = \"api.forge.example\"\nscope_prefix = \"repos\"", "unknown keys"},
		"wildcard subpaths": {`host = "api.forge.example"`, "host = \"api.forge.example\"\nwildcard_subpaths = [\"forks\"]", "unknown keys"},
		"proxy scope":       {`upstream = "https://forge.example"`, "upstream = \"https://forge.example\"\nscope_segments = 2", "unknown keys"},
		// The guest reaches the broker as 127.0.0.1: a route for a loopback,
		// IP or single-label host would capture every other cap's traffic.
		"loopback ip":      {`host = "api.forge.example"`, `host = "127.0.0.1"`, "proxy.routes[0].host"},
		"localhost":        {`host = "api.forge.example"`, `host = "localhost"`, "proxy.routes[0].host"},
		"localhost suffix": {`host = "api.forge.example"`, `host = "x.localhost"`, "proxy.routes[0].host"},
		"single label":     {`host = "api.forge.example"`, `host = "broker"`, "proxy.routes[0].host"},
	} {
		t.Run(name, func(t *testing.T) {
			text := strings.Replace(routedProxy, tc.from, tc.to, 1)
			if text == routedProxy {
				t.Fatalf("fixture replacement %q did not apply", tc.from)
			}
			_, err := Parse(text, "t")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
	dup := routedProxy + "[[proxy.routes]]\nhost = \"api.forge.example\"\nupstream = \"https://other.example\"\n"
	if _, err := Parse(dup, "t"); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate route hosts must fail, got %v", err)
	}
}

func TestSetRouteFor(t *testing.T) {
	c, err := Parse(routedProxy, "t")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	set := Set{"forge": c}
	got, r, ok := set.RouteFor("api.forge.example")
	if !ok || got.Name != "forge" || r.Upstream != "https://api.forge.example" {
		t.Fatalf("RouteFor = %v, %+v, %v", got.Name, r, ok)
	}
	if _, _, ok := set.RouteFor("forge.example"); ok {
		t.Fatalf("the main upstream is reached under /forge/, not by host")
	}
}

func TestSetRejectsHostClaimedTwice(t *testing.T) {
	a, err := Parse(routedProxy, "a")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b, err := Parse(strings.Replace(routedProxy, `name = "forge"`, `name = "forge2"`, 1), "b")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := (Set{"forge": a, "forge2": b}).Validate(); err == nil || !strings.Contains(err.Error(), "api.forge.example") ||
		!strings.Contains(err.Error(), "(b)") {
		t.Fatalf("two caps routing one host must fail, got %v", err)
	}
}

func TestSecretRefsCoverRoutes(t *testing.T) {
	c, err := Parse(routedProxy, "t")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := c.Proxy.SecretRefs()
	if strings.Join(refs, ",") != "env:T,env:API" {
		t.Fatalf("refs = %v", refs)
	}
}
