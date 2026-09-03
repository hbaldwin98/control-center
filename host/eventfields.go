package host

import (
	"reflect"
	"strings"
)

// Fields derives an event's catalog from the Go struct the plugin actually
// publishes, so the two cannot drift: names and types come from the json tags,
// and purposes come from the map, keyed by the same dotted path a notification
// rule writes.
//
// A field with no entry in purposes gets an empty purpose, which the host
// rejects when it loads the plugin. That is deliberate: adding a payload field
// and forgetting to describe it is a startup failure, not a silently
// undocumented key.
//
// Nested structs are declared as "object" and then walked, so both the object
// and its leaves are offered. A slice of structs is declared as "object[]" and
// not walked, because a rule cannot template into an unindexed list. Walking
// stops at a type already on the path, so a self-referential payload describes
// one level rather than looping forever.
func Fields(payload any, purposes map[string]string) []EventField {
	t := reflect.TypeOf(payload)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	return appendFields(nil, t, "", purposes, map[reflect.Type]bool{})
}

func appendFields(out []EventField, t reflect.Type, prefix string, purposes map[string]string, seen map[reflect.Type]bool) []EventField {
	if seen[t] {
		// A payload that contains itself has no finite path list; stop rather
		// than recurse forever.
		return out
	}
	seen[t] = true
	defer delete(seen, t)

	for i := range t.NumField() {
		field := t.Field(i)
		ft := field.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		name, ok := jsonName(field)
		if !ok {
			// An untagged embedded struct is marshaled inline, so its fields
			// belong at this level rather than under a name of their own. That
			// holds even when the embedded type is unexported, which is how a
			// plugin shares one payload section between two events.
			if field.Anonymous && ft.Kind() == reflect.Struct {
				out = appendFields(out, ft, prefix, purposes, seen)
			}
			continue
		}
		if !field.IsExported() {
			continue
		}
		path := prefix + name
		kind, nested := fieldType(ft)
		if kind == "" {
			continue
		}
		out = append(out, EventField{Name: path, Type: kind, Purpose: purposes[path]})
		if nested != nil {
			out = appendFields(out, nested, path+".", purposes, seen)
		}
	}
	return out
}

// fieldType maps a Go type onto the closed set of catalog types. The second
// result is the struct to walk into, when there is one.
func fieldType(t reflect.Type) (string, reflect.Type) {
	switch t.Kind() {
	case reflect.String:
		return "string", nil
	case reflect.Bool:
		return "boolean", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number", nil
	case reflect.Struct:
		return "object", t
	case reflect.Slice, reflect.Array:
		elem := t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		switch elem.Kind() {
		case reflect.String:
			return "string[]", nil
		case reflect.Struct:
			return "object[]", nil
		default:
			return "", nil
		}
	default:
		return "", nil
	}
}

// jsonName is the key this field is marshaled under, or ok=false when it is
// never marshaled.
func jsonName(field reflect.StructField) (string, bool) {
	tag, tagged := field.Tag.Lookup("json")
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" && !strings.HasPrefix(tag, "-,") {
		return "", false
	}
	if name == "" {
		if !tagged && field.Anonymous {
			return "", false
		}
		name = field.Name
	}
	return name, true
}
