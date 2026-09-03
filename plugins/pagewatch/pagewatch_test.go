package pagewatch

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/host/hosttest"
)

func TestValidateTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		host string
		ok   bool
	}{
		{name: "public https", raw: "https://Example.COM/path?q=1", host: "example.com", ok: true},
		{name: "explicit https port", raw: "https://example.com:443/", host: "example.com", ok: true},
		{name: "http", raw: "http://example.com/"},
		{name: "ip literal", raw: "https://127.0.0.1/"},
		{name: "ipv6 literal", raw: "https://[::1]/"},
		{name: "alternate port", raw: "https://example.com:8443/"},
		{name: "userinfo", raw: "https://user@example.com/"},
		{name: "missing host", raw: "https:///path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, gotHost, err := validateTarget(tt.raw)
			if tt.ok && err != nil {
				t.Fatalf("validateTarget() error = %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("validateTarget() unexpectedly accepted target")
			}
			if gotHost != tt.host {
				t.Fatalf("host = %q, want %q", gotHost, tt.host)
			}
		})
	}
}

func TestVisibleTextRemovesHiddenMarkup(t *testing.T) {
	t.Parallel()
	raw := `<!doctype html><html><head><style>.secret { color: red }</style></head><body>
		<h1>Example &amp; Co.</h1><!-- hidden words --><script>danger()</script>
		<p>Text <strong>people</strong> can see.</p><svg><text>diagram label</text></svg>
	</body></html>`
	if got, want := visibleText(raw), "Example & Co. Text people can see."; got != want {
		t.Fatalf("visibleText() = %q, want %q", got, want)
	}
}

func TestCheckStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, previousURL, previousHash, target, currentHash, want string
	}{
		{name: "first check", target: "https://example.com/", currentHash: "a", want: "baseline"},
		{name: "target changed", previousURL: "https://old.example/", previousHash: "a", target: "https://example.com/", currentHash: "a", want: "baseline"},
		{name: "unchanged", previousURL: "https://example.com/", previousHash: "a", target: "https://example.com/", currentHash: "a", want: "unchanged"},
		{name: "changed", previousURL: "https://example.com/", previousHash: "a", target: "https://example.com/", currentHash: "b", want: "changed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := checkStatus(tt.previousURL, tt.previousHash, tt.target, tt.currentHash); got != tt.want {
				t.Fatalf("checkStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateUTF8KeepsValidBoundary(t *testing.T) {
	t.Parallel()
	got := truncateUTF8("start café finish", 10)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateUTF8() returned invalid UTF-8: %q", got)
	}
	if len(got) > 10 {
		t.Fatalf("truncateUTF8() returned %d bytes, want at most 10", len(got))
	}
}

func TestCheckPromptFitsCheapRouteInputBound(t *testing.T) {
	t.Parallel()
	prompt := checkPrompt(
		"https://example.com/"+strings.Repeat("path/", 500),
		"attention",
		strings.Repeat("expected ", 100),
		false,
		strings.Repeat("visible page text ", 500),
	)
	if len(prompt) > maxPromptText {
		t.Fatalf("prompt is %d bytes, want at most %d", len(prompt), maxPromptText)
	}
	if !utf8.ValidString(prompt) {
		t.Fatal("prompt is not valid UTF-8")
	}
}

func TestPluginContract(t *testing.T) {
	t.Parallel()
	p := New()
	manifest := p.Manifest()
	if manifest.ID != pluginID || !manifest.Automated {
		t.Fatalf("manifest = %#v, want automated %q plugin", manifest, pluginID)
	}
	var defaults settings
	if err := json.Unmarshal(manifest.Config.Defaults, &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults.URL != "https://example.com/" || defaults.ExpectedText != "Example Domain" {
		t.Fatalf("defaults = %#v", defaults)
	}
	jobs := p.Jobs()
	if len(jobs) != 1 || jobs[0].Name != "check" || jobs[0].Schedule != checkSchedule {
		t.Fatalf("jobs = %#v", jobs)
	}
	routes := p.Routes()
	if len(routes) != 2 || routes[0].Pattern != "GET /checks" || routes[1].Pattern != "POST /checks" {
		t.Fatalf("routes = %#v", routes)
	}
	subs := p.Subscriptions()
	if len(subs) != 1 || subs[0].Pattern != "pagewatch.check.completed" || subs[0].Durable == nil {
		t.Fatalf("subscriptions = %#v", subs)
	}
	if len(manifest.Events) != 2 || manifest.Events[0].Type != "check.completed" || manifest.Events[1].Type != "alert" {
		t.Fatalf("events = %#v", manifest.Events)
	}

	m := &captureMigrator{}
	if err := p.Migrate(m); err != nil {
		t.Fatal(err)
	}
	if len(m.migrations) != 1 || !strings.Contains(m.migrations[0].Up, "pagewatch_checks") {
		t.Fatalf("migrations = %#v", m.migrations)
	}
}

type captureMigrator struct {
	migrations []host.Migration
}

func (m *captureMigrator) Apply(migrations []host.Migration) error {
	m.migrations = migrations
	return nil
}

func TestCatalogMatchesThePayloads(t *testing.T) {
	t.Parallel()
	hosttest.CheckEventCatalog(t, New().Manifest(), map[string]any{
		"check.completed": checkEvent{
			CheckedAt: "2026-09-02T00:00:00Z", URL: "https://example.test", Status: "changed",
			ContentHash: "abc", ExpectedText: "hello", ExpectedFound: true, Summary: "s",
			AIRan: true, InputTokens: 1, OutputTokens: 2, CostMicroUSD: 3, BrowserMS: 4, AIMS: 5, JobID: 6,
		},
		"alert": alertEvent{Title: "t", Body: "b"},
	})
}
