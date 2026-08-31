package pluginhost

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var secretName = regexp.MustCompile(`(?i)(password|secret|token|api[_-]?key|private[_-]?key|client_secret)`)

var allowedSchemaKeys = map[string]struct{}{
	"$schema": {}, "type": {}, "properties": {}, "required": {}, "additionalProperties": {},
	"items": {}, "enum": {}, "minimum": {}, "maximum": {}, "exclusiveMinimum": {},
	"exclusiveMaximum": {}, "minLength": {}, "maxLength": {}, "pattern": {},
	"minItems": {}, "maxItems": {}, "description": {}, "title": {}, "default": {},
}

func validateConfigSpec(spec hostConfigSpec) error {
	if len(spec.Schema) == 0 || string(spec.Schema) == "null" {
		if len(spec.Defaults) == 0 || string(spec.Defaults) == "null" {
			return nil
		}
		return fmt.Errorf("%w: defaults require a schema", ErrInvalidConfig)
	}
	var schema any
	if err := json.Unmarshal(spec.Schema, &schema); err != nil {
		return fmt.Errorf("%w: schema is not JSON: %v", ErrInvalidConfig, err)
	}
	if err := checkSchema(schema, ""); err != nil {
		return err
	}
	defaults := spec.Defaults
	if len(defaults) == 0 || string(defaults) == "null" {
		defaults = []byte(`{}`)
	}
	var value any
	if err := json.Unmarshal(defaults, &value); err != nil {
		return fmt.Errorf("%w: defaults are not JSON: %v", ErrInvalidConfig, err)
	}
	return validateAgainst(schema, value, "")
}

type hostConfigSpec struct {
	Schema   json.RawMessage
	Defaults json.RawMessage
}

func checkSchema(node any, path string) error {
	obj, ok := node.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: schema at %s must be an object", ErrInvalidConfig, loc(path))
	}
	for k := range obj {
		if _, ok := allowedSchemaKeys[k]; !ok {
			return fmt.Errorf("%w: unsupported schema keyword %q at %s", ErrInvalidConfig, k, loc(path))
		}
	}
	if t, ok := obj["type"].(string); ok {
		switch t {
		case "object", "array", "string", "number", "integer", "boolean":
		default:
			return fmt.Errorf("%w: unsupported type %q at %s", ErrInvalidConfig, t, loc(path))
		}
	}
	if props, ok := obj["properties"].(map[string]any); ok {
		for name, sub := range props {
			if secretName.MatchString(name) {
				return fmt.Errorf("%w: property %q", ErrSecretConfig, name)
			}
			if err := checkSchema(sub, path+"/"+name); err != nil {
				return err
			}
		}
	}
	if items, ok := obj["items"]; ok {
		if err := checkSchema(items, path+"/*"); err != nil {
			return err
		}
	}
	if add, ok := obj["additionalProperties"]; ok {
		switch add := add.(type) {
		case bool:
		case map[string]any:
			if err := checkSchema(add, path+"/*"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: additionalProperties at %s", ErrInvalidConfig, loc(path))
		}
	}
	return nil
}

func validateAgainst(schema, value any, path string) error {
	obj, ok := schema.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: schema at %s", ErrInvalidConfig, loc(path))
	}
	if t, ok := obj["type"].(string); ok {
		if err := checkType(t, value, path); err != nil {
			return err
		}
	}
	if enums, ok := obj["enum"].([]any); ok {
		matched := false
		for _, e := range enums {
			if jsonEqual(e, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%w: value at %s is not in enum", ErrInvalidConfig, loc(path))
		}
	}
	switch v := value.(type) {
	case map[string]any:
		props, _ := obj["properties"].(map[string]any)
		if req, ok := obj["required"].([]any); ok {
			for _, r := range req {
				name, _ := r.(string)
				if _, ok := v[name]; !ok {
					return fmt.Errorf("%w: missing required %q at %s", ErrInvalidConfig, name, loc(path))
				}
			}
		}
		add := obj["additionalProperties"]
		for name, child := range v {
			if secretName.MatchString(name) {
				return fmt.Errorf("%w: property %q", ErrSecretConfig, name)
			}
			if sub, ok := props[name]; ok {
				if err := validateAgainst(sub, child, path+"/"+name); err != nil {
					return err
				}
				continue
			}
			switch add := add.(type) {
			case bool:
				if !add {
					return fmt.Errorf("%w: unexpected property %q at %s", ErrInvalidConfig, name, loc(path))
				}
			case map[string]any:
				if err := validateAgainst(add, child, path+"/"+name); err != nil {
					return err
				}
			}
		}
	case []any:
		if items, ok := obj["items"]; ok {
			for i, child := range v {
				if err := validateAgainst(items, child, fmt.Sprintf("%s/%d", path, i)); err != nil {
					return err
				}
			}
		}
		if n, ok := asInt(obj["minItems"]); ok && len(v) < n {
			return fmt.Errorf("%w: minItems at %s", ErrInvalidConfig, loc(path))
		}
		if n, ok := asInt(obj["maxItems"]); ok && len(v) > n {
			return fmt.Errorf("%w: maxItems at %s", ErrInvalidConfig, loc(path))
		}
	case string:
		if n, ok := asInt(obj["minLength"]); ok && len(v) < n {
			return fmt.Errorf("%w: minLength at %s", ErrInvalidConfig, loc(path))
		}
		if n, ok := asInt(obj["maxLength"]); ok && len(v) > n {
			return fmt.Errorf("%w: maxLength at %s", ErrInvalidConfig, loc(path))
		}
		if p, ok := obj["pattern"].(string); ok {
			re, err := regexp.Compile(p)
			if err != nil {
				return fmt.Errorf("%w: pattern at %s: %v", ErrInvalidConfig, loc(path), err)
			}
			if !re.MatchString(v) {
				return fmt.Errorf("%w: pattern at %s", ErrInvalidConfig, loc(path))
			}
		}
	case float64:
		if n, ok := asFloat(obj["minimum"]); ok && v < n {
			return fmt.Errorf("%w: minimum at %s", ErrInvalidConfig, loc(path))
		}
		if n, ok := asFloat(obj["maximum"]); ok && v > n {
			return fmt.Errorf("%w: maximum at %s", ErrInvalidConfig, loc(path))
		}
	}
	return nil
}

func checkType(want string, value any, path string) error {
	ok := false
	switch want {
	case "object":
		_, ok = value.(map[string]any)
	case "array":
		_, ok = value.([]any)
	case "string":
		_, ok = value.(string)
	case "number":
		_, ok = value.(float64)
	case "integer":
		f, is := value.(float64)
		ok = is && f == float64(int64(f))
	case "boolean":
		_, ok = value.(bool)
	}
	if !ok {
		return fmt.Errorf("%w: expected %s at %s", ErrInvalidConfig, want, loc(path))
	}
	return nil
}

func loc(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

func asInt(v any) (int, bool) {
	f, ok := asFloat(v)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return strings.TrimSpace(string(ab)) == strings.TrimSpace(string(bb))
}
