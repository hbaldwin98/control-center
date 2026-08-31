package browser

import "testing"

func TestParseSelector(t *testing.T) {
	cases := []struct {
		in   string
		tag  string
		id   string
		cls  string
		attr string
		val  string
		op   string
	}{
		{in: "article", tag: "article"},
		{in: "#email", id: "email"},
		{in: ".lot", cls: "lot"},
		{in: "article.lot", tag: "article", cls: "lot"},
		{in: "input[type=password]", tag: "input", attr: "type", val: "password", op: "eq"},
		{in: `input[formControlName=email]`, tag: "input", attr: "formcontrolname", val: "email", op: "eq"},
		{in: "button[mat-flat-button]", tag: "button", attr: "mat-flat-button", val: "", op: "exists"},
		{in: `input[type="password"]`, tag: "input", attr: "type", val: "password", op: "eq"},
		{in: "button[type=submit]", tag: "button", attr: "type", val: "submit", op: "eq"},
		{in: "[name=username]", attr: "name", val: "username", op: "eq"},
	}
	for _, c := range cases {
		s, err := parseSelector(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if s.tag != c.tag || s.id != c.id || s.class != c.cls || s.attr != c.attr || s.attrVal != c.val || s.attrOp != c.op {
			t.Errorf("%s: %+v", c.in, s)
		}
	}
}

func TestHTMLMatchesAttributeSelector(t *testing.T) {
	doc := `<form><input type="email" name="user"><input type="password" name="pass"><button type="submit">Go</button></form>`
	ok, err := htmlMatches(doc, `input[type=password]`)
	if err != nil || !ok {
		t.Fatalf("password: %v %v", ok, err)
	}
	ok, err = htmlMatches(doc, `button[type=submit]`)
	if err != nil || !ok {
		t.Fatalf("submit: %v %v", ok, err)
	}
	ok, err = htmlMatches(doc, `#missing`)
	if err != nil || ok {
		t.Fatalf("missing: %v %v", ok, err)
	}

	angular := `<form><input type="email" formControlName="email"><input type="password" formControlName="password"><button mat-flat-button color="primary" class="w-100">Sign in</button></form>`
	ok, err = htmlMatches(angular, `input[formControlName=email]`)
	if err != nil || !ok {
		t.Fatalf("formControlName: %v %v", ok, err)
	}
	ok, err = htmlMatches(angular, `button[mat-flat-button]`)
	if err != nil || !ok {
		t.Fatalf("mat-flat-button: %v %v", ok, err)
	}
}
