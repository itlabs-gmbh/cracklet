package guest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func parseJSONObject(text string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" {
		return map[string]any{}, nil
	}
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("existing file is not a JSON object: %w", err)
	}
	return doc, nil
}

func marshalJSON(doc map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// setKey returns a copy of doc with the dotted key set, creating objects on
// the way; a non-object in the way is replaced.
func setKey(doc map[string]any, key string, value any) map[string]any {
	out := copyMap(doc)
	head, rest, more := strings.Cut(key, ".")
	if !more {
		out[head] = value
		return out
	}
	child, _ := out[head].(map[string]any)
	out[head] = setKey(child, rest, value)
	return out
}

// deleteKey returns a copy of doc without the dotted key; missing keys are a no-op.
func deleteKey(doc map[string]any, key string) map[string]any {
	out := copyMap(doc)
	head, rest, more := strings.Cut(key, ".")
	if !more {
		delete(out, head)
		return out
	}
	child, ok := out[head].(map[string]any)
	if !ok {
		return out
	}
	out[head] = deleteKey(child, rest)
	return out
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}
