package pluginhost

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// decode turns a JSON literal into the `any` shape validateAgainst walks.
func decode(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("bad test literal %q: %v", raw, err)
	}
	return v
}

func TestValidateAgainstTypes(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		value  string
		want   error
	}{
		{"object ok", `{"type":"object"}`, `{}`, nil},
		{"object rejects array", `{"type":"object"}`, `[]`, ErrInvalidConfig},
		{"array ok", `{"type":"array"}`, `[]`, nil},
		{"array rejects object", `{"type":"array"}`, `{}`, ErrInvalidConfig},
		{"string ok", `{"type":"string"}`, `"x"`, nil},
		{"string rejects number", `{"type":"string"}`, `1`, ErrInvalidConfig},
		{"number ok", `{"type":"number"}`, `1.5`, nil},
		{"number rejects string", `{"type":"number"}`, `"1"`, ErrInvalidConfig},
		{"integer ok", `{"type":"integer"}`, `3`, nil},
		{"integer rejects fraction", `{"type":"integer"}`, `3.5`, ErrInvalidConfig},
		{"integer rejects bool", `{"type":"integer"}`, `true`, ErrInvalidConfig},
		{"boolean ok", `{"type":"boolean"}`, `true`, nil},
		{"boolean rejects string", `{"type":"boolean"}`, `"true"`, ErrInvalidConfig},
		{"unknown type is unchecked", `{"type":"widget"}`, `"anything"`, ErrInvalidConfig},
		{"null value fails a typed schema", `{"type":"string"}`, `null`, ErrInvalidConfig},
		{"untyped schema accepts anything", `{}`, `[1,"two",null]`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgainst(decode(t, tc.schema), decode(t, tc.value), "")
			assertErr(t, err, tc.want)
		})
	}
}

func TestValidateAgainstNonObjectSchema(t *testing.T) {
	// A schema node that is not a JSON object cannot describe anything.
	for _, raw := range []string{`true`, `"string"`, `[]`, `null`, `1`} {
		if err := validateAgainst(decode(t, raw), decode(t, `{}`), ""); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("schema %s: got %v", raw, err)
		}
	}
}

func TestValidateAgainstEnum(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		value  string
		want   error
	}{
		{"member", `{"enum":["a","b"]}`, `"a"`, nil},
		{"non member", `{"enum":["a","b"]}`, `"c"`, ErrInvalidConfig},
		{"structural equality", `{"enum":[{"k":[1,2]}]}`, `{"k":[1,2]}`, nil},
		{"structural mismatch", `{"enum":[{"k":[1,2]}]}`, `{"k":[2,1]}`, ErrInvalidConfig},
		{"number member", `{"enum":[1,2,3]}`, `2`, nil},
		{"null member", `{"enum":[null]}`, `null`, nil},
		{"empty enum rejects all", `{"enum":[]}`, `"a"`, ErrInvalidConfig},
		{"non-array enum is ignored", `{"enum":"a"}`, `"b"`, nil},
		{"type checked before enum", `{"type":"string","enum":["a"]}`, `1`, ErrInvalidConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgainst(decode(t, tc.schema), decode(t, tc.value), "")
			assertErr(t, err, tc.want)
		})
	}
}

func TestValidateAgainstObject(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		value  string
		want   error
	}{
		{
			"required present",
			`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`,
			`{"a":"x"}`, nil,
		},
		{
			"required missing",
			`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`,
			`{}`, ErrInvalidConfig,
		},
		{
			"required entry that is not a string can never be satisfied",
			`{"type":"object","required":[1]}`,
			`{"1":"x"}`, ErrInvalidConfig,
		},
		{
			"known property is validated against its subschema",
			`{"type":"object","properties":{"a":{"type":"integer"}}}`,
			`{"a":"nope"}`, ErrInvalidConfig,
		},
		{
			"nested property error surfaces",
			`{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"integer"}}}}}`,
			`{"a":{"b":1.5}}`, ErrInvalidConfig,
		},
		{
			"unknown property rejected when additionalProperties is false",
			`{"type":"object","properties":{"a":{}},"additionalProperties":false}`,
			`{"b":1}`, ErrInvalidConfig,
		},
		{
			"unknown property allowed when additionalProperties is true",
			`{"type":"object","properties":{"a":{}},"additionalProperties":true}`,
			`{"b":1}`, nil,
		},
		{
			"unknown property allowed when additionalProperties is absent",
			`{"type":"object","properties":{"a":{}}}`,
			`{"b":1}`, nil,
		},
		{
			"additionalProperties schema accepts a conforming value",
			`{"type":"object","additionalProperties":{"type":"integer"}}`,
			`{"b":1}`, nil,
		},
		{
			"additionalProperties schema rejects a non-conforming value",
			`{"type":"object","additionalProperties":{"type":"integer"}}`,
			`{"b":"x"}`, ErrInvalidConfig,
		},
		{
			"declared property wins over additionalProperties false",
			`{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false}`,
			`{"a":"x"}`, nil,
		},
		{"secret-named property rejected", `{"type":"object"}`, `{"api_key":"x"}`, ErrSecretConfig},
		{"secret name matched case-insensitively", `{"type":"object"}`, `{"API-Key":"x"}`, ErrSecretConfig},
		{"secret name matched as a substring", `{"type":"object"}`, `{"db_password_file":"x"}`, ErrSecretConfig},
		{
			"secret-named property rejected below the root",
			`{"type":"object","properties":{"a":{"type":"object"}}}`,
			`{"a":{"client_secret":"x"}}`, ErrSecretConfig,
		},
		{"innocuous name allowed", `{"type":"object"}`, `{"keyring_path":"x"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgainst(decode(t, tc.schema), decode(t, tc.value), "")
			assertErr(t, err, tc.want)
		})
	}
}

func TestValidateAgainstArray(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		value  string
		want   error
	}{
		{"items accept", `{"type":"array","items":{"type":"integer"}}`, `[1,2]`, nil},
		{"items reject", `{"type":"array","items":{"type":"integer"}}`, `[1,"x"]`, ErrInvalidConfig},
		{"items absent", `{"type":"array"}`, `[1,"x",null]`, nil},
		{"minItems satisfied", `{"type":"array","minItems":2}`, `[1,2]`, nil},
		{"minItems violated", `{"type":"array","minItems":2}`, `[1]`, ErrInvalidConfig},
		{"maxItems satisfied", `{"type":"array","maxItems":2}`, `[1,2]`, nil},
		{"maxItems violated", `{"type":"array","maxItems":2}`, `[1,2,3]`, ErrInvalidConfig},
		{"non-numeric bounds ignored", `{"type":"array","minItems":"2"}`, `[]`, nil},
		{
			"nested array error surfaces",
			`{"type":"array","items":{"type":"array","items":{"type":"integer"}}}`,
			`[[1],[2,"x"]]`, ErrInvalidConfig,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgainst(decode(t, tc.schema), decode(t, tc.value), "")
			assertErr(t, err, tc.want)
		})
	}
}

func TestValidateAgainstString(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		value  string
		want   error
	}{
		{"minLength satisfied", `{"type":"string","minLength":2}`, `"ab"`, nil},
		{"minLength violated", `{"type":"string","minLength":2}`, `"a"`, ErrInvalidConfig},
		{"maxLength satisfied", `{"type":"string","maxLength":2}`, `"ab"`, nil},
		{"maxLength violated", `{"type":"string","maxLength":2}`, `"abc"`, ErrInvalidConfig},
		{"pattern matched", `{"type":"string","pattern":"^a+$"}`, `"aaa"`, nil},
		{"pattern unmatched", `{"type":"string","pattern":"^a+$"}`, `"b"`, ErrInvalidConfig},
		{"pattern is unanchored by default", `{"type":"string","pattern":"a"}`, `"xax"`, nil},
		{"uncompilable pattern", `{"type":"string","pattern":"a("}`, `"a"`, ErrInvalidConfig},
		{"non-string pattern ignored", `{"type":"string","pattern":1}`, `"b"`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgainst(decode(t, tc.schema), decode(t, tc.value), "")
			assertErr(t, err, tc.want)
		})
	}
}

func TestValidateAgainstNumber(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		value  string
		want   error
	}{
		{"minimum satisfied", `{"type":"number","minimum":2}`, `2`, nil},
		{"minimum violated", `{"type":"number","minimum":2}`, `1.9`, ErrInvalidConfig},
		{"maximum satisfied", `{"type":"number","maximum":2}`, `2`, nil},
		{"maximum violated", `{"type":"number","maximum":2}`, `2.1`, ErrInvalidConfig},
		{"bounds apply to integers too", `{"type":"integer","minimum":2}`, `1`, ErrInvalidConfig},
		{"non-numeric bound ignored", `{"type":"number","minimum":"2"}`, `1`, nil},
		{"bounds do not apply to strings", `{"type":"string","minimum":2}`, `"a"`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAgainst(decode(t, tc.schema), decode(t, tc.value), "")
			assertErr(t, err, tc.want)
		})
	}
}

// The error path is the part a plugin author reads, so it has to name the
// offending node rather than just the root.
func TestValidateAgainstErrorNamesThePath(t *testing.T) {
	schema := decode(t, `{"type":"object","properties":{"a":{"type":"array","items":{"type":"object","properties":{"b":{"type":"integer"}}}}}}`)
	err := validateAgainst(schema, decode(t, `{"a":[{"b":1},{"b":"x"}]}`), "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "/a/1/b") {
		t.Fatalf("error should point at /a/1/b, got %q", err.Error())
	}
}

func TestValidateAgainstRootPathRendersAsSlash(t *testing.T) {
	err := validateAgainst(decode(t, `{"type":"string"}`), decode(t, `1`), "")
	if err == nil || !strings.Contains(err.Error(), " /") {
		t.Fatalf("root error should render the path as /, got %v", err)
	}
}

func TestCheckSchemaRejectsUnsupportedShapes(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		want   error
	}{
		{"ref keyword", `{"$ref":"#/defs/x"}`, ErrInvalidConfig},
		{"allOf keyword", `{"allOf":[{"type":"string"}]}`, ErrInvalidConfig},
		{"unsupported type", `{"type":"null"}`, ErrInvalidConfig},
		{"non-object schema", `"string"`, ErrInvalidConfig},
		{"unsupported keyword nested in properties", `{"properties":{"a":{"$ref":"x"}}}`, ErrInvalidConfig},
		{"unsupported keyword nested in items", `{"items":{"$ref":"x"}}`, ErrInvalidConfig},
		{"unsupported keyword nested in additionalProperties", `{"additionalProperties":{"$ref":"x"}}`, ErrInvalidConfig},
		{"non-object non-bool additionalProperties", `{"additionalProperties":"yes"}`, ErrInvalidConfig},
		{"secret property name", `{"properties":{"api_key":{"type":"string"}}}`, ErrSecretConfig},
		{"secret property name nested", `{"properties":{"a":{"properties":{"token":{}}}}}`, ErrSecretConfig},
		{"bool additionalProperties allowed", `{"additionalProperties":false}`, nil},
		{"schema additionalProperties allowed", `{"additionalProperties":{"type":"string"}}`, nil},
		{"full supported keyword set", `{"$schema":"x","type":"object","title":"t","description":"d","default":{},"properties":{"a":{"type":"integer","minimum":0,"maximum":5,"exclusiveMinimum":0,"exclusiveMaximum":6,"enum":[1,2]}},"required":["a"],"additionalProperties":false}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertErr(t, checkSchema(decode(t, tc.schema), ""), tc.want)
		})
	}
}

func TestValidateConfigSpec(t *testing.T) {
	cases := []struct {
		name     string
		schema   string
		defaults string
		want     error
	}{
		{"no schema and no defaults", ``, ``, nil},
		{"explicit JSON nulls", `null`, `null`, nil},
		{"defaults without a schema", ``, `{"a":1}`, ErrInvalidConfig},
		{"schema without defaults", `{"type":"object"}`, ``, nil},
		{"schema that is not JSON", `{`, `{}`, ErrInvalidConfig},
		{"defaults that are not JSON", `{"type":"object"}`, `{`, ErrInvalidConfig},
		{"schema rejected by checkSchema", `{"$ref":"x"}`, `{}`, ErrInvalidConfig},
		{
			"defaults that violate the schema",
			`{"type":"object","properties":{"a":{"type":"integer"}}}`, `{"a":"x"}`, ErrInvalidConfig,
		},
		{
			"defaults that satisfy the schema",
			`{"type":"object","properties":{"a":{"type":"integer"}},"required":["a"]}`, `{"a":1}`, nil,
		},
		{
			"omitted defaults are validated as an empty object",
			`{"type":"object","required":["a"]}`, ``, ErrInvalidConfig,
		},
		{"secret in the schema", `{"type":"object","properties":{"token":{"type":"string"}}}`, `{}`, ErrSecretConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := hostConfigSpec{}
			if tc.schema != "" {
				spec.Schema = json.RawMessage(tc.schema)
			}
			if tc.defaults != "" {
				spec.Defaults = json.RawMessage(tc.defaults)
			}
			assertErr(t, validateConfigSpec(spec), tc.want)
		})
	}
}

func TestAsFloatAcceptsJSONNumber(t *testing.T) {
	// Schemas decoded with a json.Decoder in UseNumber mode carry json.Number
	// bounds rather than float64 ones.
	cases := []struct {
		in     any
		want   float64
		wantOK bool
	}{
		{float64(2.5), 2.5, true},
		{json.Number("2.5"), 2.5, true},
		{json.Number("nope"), 0, false},
		{"2.5", 0, false},
		{nil, 0, false},
	}
	for _, tc := range cases {
		got, ok := asFloat(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("asFloat(%#v) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
	if n, ok := asInt(json.Number("3.9")); n != 3 || !ok {
		t.Fatalf("asInt truncates toward zero: got %v, %v", n, ok)
	}
	if _, ok := asInt("x"); ok {
		t.Fatal("asInt should reject a non-number")
	}
}

func assertErr(t *testing.T, got, want error) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Fatalf("unexpected error: %v", got)
	case want != nil && !errors.Is(got, want):
		t.Fatalf("got %v, want %v", got, want)
	}
}
