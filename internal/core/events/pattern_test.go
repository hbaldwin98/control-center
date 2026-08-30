package events

import "testing"

func TestPatternMatching(t *testing.T) {
	cases := []struct {
		pattern string
		typ     string
		want    bool
	}{
		{"**", "core.job.failed", true},
		{"**", "a.b", true},
		{"bidrl.**", "bidrl.deal_found", true},
		{"bidrl.**", "bidrl.scan.started", true},
		{"bidrl.**", "core.job.failed", false},
		{"core.job.*", "core.job.failed", true},
		{"core.job.*", "core.job.a.b", false},
		{"**.failed", "core.job.failed", true},
		{"**.failed", "bidrl.scan.failed", true},
		{"**.failed", "core.job.succeeded", false},
		{"core.*.failed", "core.job.failed", true},
		{"core.*.failed", "core.a.b.failed", false},
		{"core.**.failed", "core.a.b.failed", true},
		{"core.**.failed", "core.failed", true},
		{"a.b", "a.b", true},
		{"a.b", "a.b.c", false},
		{"a.b.**", "a.b", true},
	}
	for _, c := range cases {
		p, err := CompilePattern(c.pattern)
		if err != nil {
			t.Fatalf("compile %q: %v", c.pattern, err)
		}
		if got := p.Matches(c.typ); got != c.want {
			t.Errorf("%q matches %q = %v, want %v", c.pattern, c.typ, got, c.want)
		}
	}
}

func TestPatternValidation(t *testing.T) {
	for _, bad := range []string{"", "Core.job", "core..job", "core.job-x", "core.*x", "1core.job", "core.***"} {
		if _, err := CompilePattern(bad); err == nil {
			t.Errorf("CompilePattern(%q) should fail", bad)
		}
	}
	for _, good := range []string{"**", "core.**", "*.job.*", "bidrl.deal_found", "a1.b2_c"} {
		if _, err := CompilePattern(good); err != nil {
			t.Errorf("CompilePattern(%q) = %v", good, err)
		}
	}
}

func TestTypeValidation(t *testing.T) {
	bad := []string{"", "core", "Core.job", "core.", ".job", "core..job", "core.Job", "core.job-x", "1a.b"}
	for _, s := range bad {
		if err := validateType(s); err == nil {
			t.Errorf("validateType(%q) should fail", s)
		}
	}
	for _, s := range []string{"core.job", "core.job.failed", "bidrl.deal_found", "a.b_c.d1"} {
		if err := validateType(s); err != nil {
			t.Errorf("validateType(%q) = %v", s, err)
		}
	}
}

func TestSourceMustOwnItsNamespace(t *testing.T) {
	ok := []struct{ source, typ string }{
		{"core.jobs", "core.job.failed"},
		{"core.ai", "core.ai.usage"},
		{"bidrl", "bidrl.deal_found"},
	}
	for _, c := range ok {
		if err := validateSource(c.source, c.typ); err != nil {
			t.Errorf("validateSource(%q, %q) = %v", c.source, c.typ, err)
		}
	}

	bad := []struct{ source, typ string }{
		{"bidrl", "core.job.failed"},   // a plugin cannot forge a core event
		{"core.jobs", "bidrl.scanned"}, // nor a core module a plugin event
		{"core", "core.job.failed"},    // core sources must name a module
		{"core.a.b", "core.job.failed"},
		{"", "core.job.failed"},
		{"Bidrl", "bidrl.deal_found"},
	}
	for _, c := range bad {
		if err := validateSource(c.source, c.typ); err == nil {
			t.Errorf("validateSource(%q, %q) should fail", c.source, c.typ)
		}
	}
}

func TestRetryDelayIsExponentialAndCapped(t *testing.T) {
	p := DefaultRetry
	if got := p.delay(1); got != p.Initial {
		t.Errorf("delay(1) = %v, want %v", got, p.Initial)
	}
	if got := p.delay(2); got != 2*p.Initial {
		t.Errorf("delay(2) = %v, want %v", got, 2*p.Initial)
	}
	if got := p.delay(50); got != p.Maximum {
		t.Errorf("delay(50) = %v, want the cap %v", got, p.Maximum)
	}
}

func TestSQLPrefix(t *testing.T) {
	cases := map[string]string{
		"**":               "",
		"*.job":            "",
		"core.**":          "core",
		"core.job.*":       "core.job",
		"bidrl.deal_found": "bidrl.deal_found",
	}
	for pattern, want := range cases {
		p, err := CompilePattern(pattern)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.sqlPrefix(); got != want {
			t.Errorf("sqlPrefix(%q) = %q, want %q", pattern, got, want)
		}
	}
}
