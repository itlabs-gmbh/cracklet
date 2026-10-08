package cap

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"text/template"
)

// TemplateData is what guest-side templates can reference.
type TemplateData struct {
	// VM is the microVM name.
	VM string
	// BrokerURL is where the broker is reachable from inside the guest.
	BrokerURL string
	// PseudoToken is a per-VM placeholder for clients that insist on a token;
	// the broker never checks it, the tunnel is the identity.
	PseudoToken string
	// BrokerSocket is a Unix socket in the guest that reaches the broker, for
	// clients that cannot be given BrokerURL (gh's http_unix_socket).
	BrokerSocket string
}

// SecretFunc resolves a secret reference such as keychain:cracklet/claude-token.
type SecretFunc func(ref string) (string, error)

// Render executes a broker-side template (for example a proxy header) with
// the given secret resolver.
func Render(text string, data TemplateData, secret SecretFunc) (string, error) {
	return render(text, data, secret)
}

// RenderGuest executes a guest-side template; secrets are not available there
// because the guest must never receive them.
func RenderGuest(text string, data TemplateData) (string, error) {
	return render(text, data, guestSecretUnavailable)
}

// RenderValue walks a TOML value (maps, lists, scalars) and renders every string.
func RenderValue(v any, data TemplateData) (any, error) {
	switch x := v.(type) {
	case string:
		return RenderGuest(x, data)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			r, err := RenderValue(val, data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			r, err := RenderValue(val, data)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

func guestSecretUnavailable(string) (string, error) {
	return "", fmt.Errorf("secret is not available in guest sections")
}

func render(text string, data TemplateData, secret SecretFunc) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	tmpl, err := template.New("cap").Option("missingkey=error").Funcs(funcs(secret)).Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", unwrapExec(err)
	}
	return buf.String(), nil
}

// funcs is the function set every template sees; secret differs between the
// broker side, the guest side and SecretRefs' static walk.
func funcs(secret any) template.FuncMap {
	return template.FuncMap{"secret": secret, "basicauth": basicAuth}
}

// basicAuth encodes an HTTP Basic credential. The password comes last so a
// secret can be piped in: {{ secret "ref" | basicauth "user" }}.
func basicAuth(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}

// unwrapExec strips text/template's "template: cap:1:10: executing ... error
// calling secret:" framing so the user sees the resolver's message.
func unwrapExec(err error) error {
	var execErr template.ExecError
	if !errors.As(err, &execErr) {
		return err
	}
	if inner := errors.Unwrap(execErr.Err); inner != nil {
		return inner
	}
	return execErr.Err
}

// lintData has every field set so missingkey=error catches typos.
var lintData = TemplateData{VM: "lint", BrokerURL: "http://127.0.0.1:1", PseudoToken: "lint", BrokerSocket: "/run/lint.sock"}

// checkTemplate parses and dry-runs a template. Broker-side templates may use
// secret (resolved to a placeholder); guest-side ones must not.
func checkTemplate(text string, brokerSide bool) error {
	secret := guestSecretUnavailable
	if brokerSide {
		secret = func(ref string) (string, error) {
			if ref == "" {
				return "", fmt.Errorf("secret reference must not be empty")
			}
			return "placeholder", nil
		}
	}
	_, err := render(text, lintData, secret)
	return err
}

func checkValueTemplates(v any, brokerSide bool) error {
	switch x := v.(type) {
	case string:
		return checkTemplate(x, brokerSide)
	case map[string]any:
		for k, val := range x {
			if err := checkValueTemplates(val, brokerSide); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	case []any:
		for i, val := range x {
			if err := checkValueTemplates(val, brokerSide); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
	}
	return nil
}
