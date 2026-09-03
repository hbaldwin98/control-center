package host

import "testing"

type leaf struct {
	Day   string   `json:"day"`
	KWh   float64  `json:"kwh"`
	Cents *int64   `json:"cents,omitempty"`
	Tags  []string `json:"tags"`
}

type shared struct {
	Source string `json:"source"`
}

type sample struct {
	shared
	At       string            `json:"at"`
	Rows     int               `json:"rows"`
	OK       bool              `json:"ok"`
	Settled  *leaf             `json:"settled,omitempty"`
	Recent   []leaf            `json:"recent"`
	Hidden   string            `json:"-"`
	internal string            //nolint:unused // unexported fields are never marshaled
	Opaque   map[string]string `json:"opaque"`
}

func TestFieldsFollowsTheStruct(t *testing.T) {
	got := Fields(sample{}, map[string]string{"at": "when it happened", "settled.kwh": "kWh that day"})
	byName := map[string]EventField{}
	var order []string
	for _, f := range got {
		byName[f.Name] = f
		order = append(order, f.Name)
	}

	for name, want := range map[string]string{
		"source":        "string",
		"at":            "string",
		"rows":          "number",
		"ok":            "boolean",
		"settled":       "object",
		"settled.day":   "string",
		"settled.kwh":   "number",
		"settled.cents": "number",
		"settled.tags":  "string[]",
		"recent":        "object[]",
	} {
		f, ok := byName[name]
		if !ok {
			t.Fatalf("field %q is missing from %v", name, order)
		}
		if f.Type != want {
			t.Fatalf("field %q type = %q, want %q", name, f.Type, want)
		}
	}

	// A slice of structs is a list a rule cannot index into, so its leaves are
	// not offered.
	if _, ok := byName["recent.day"]; ok {
		t.Fatal("recent[] should not be walked")
	}
	for _, skipped := range []string{"Hidden", "-", "internal", "opaque"} {
		if _, ok := byName[skipped]; ok {
			t.Fatalf("%q is not marshaled and should not be declared", skipped)
		}
	}
	if byName["at"].Purpose != "when it happened" || byName["settled.kwh"].Purpose != "kWh that day" {
		t.Fatalf("purposes did not reach the fields: %+v", got)
	}
	// An undescribed field carries an empty purpose, which validation rejects
	// when the plugin loads. That is the drift alarm.
	if byName["rows"].Purpose != "" {
		t.Fatalf("rows purpose = %q, want empty", byName["rows"].Purpose)
	}
}

type cyclic struct {
	Name string  `json:"name"`
	Next *cyclic `json:"next,omitempty"`
}

func TestFieldsStopsOnACycle(t *testing.T) {
	// Walking stops at a type already on the path, so the payload is described
	// one level deep instead of forever.
	got := Fields(cyclic{}, nil)
	if len(got) != 2 || got[0].Name != "name" || got[1].Name != "next" {
		t.Fatalf("fields = %+v, want name and next", got)
	}
}

func TestFieldsIgnoresNonStructs(t *testing.T) {
	if got := Fields("nope", nil); got != nil {
		t.Fatalf("fields = %+v, want nil", got)
	}
	if got := Fields((*sample)(nil), nil); len(got) == 0 {
		t.Fatal("a typed nil pointer still describes its struct")
	}
}
