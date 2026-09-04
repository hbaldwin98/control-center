package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsAValidLoopbackConfig(t *testing.T) {
	cfg := Default()
	cfg.derive()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.TLSEnabled() {
		t.Fatal("loopback defaults must not require TLS")
	}
	if !cfg.LoopbackOnly() {
		t.Fatal("defaults must be loopback-only")
	}
	if cfg.Data.DB == "" || cfg.Data.BlobDir == "" {
		t.Fatal("derive must fill data paths")
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != Default().Server.Addr {
		t.Fatalf("addr = %q", cfg.Server.Addr)
	}
}

func TestLoadParsesYAMLAndRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  addr: \"127.0.0.1:9090\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "127.0.0.1:9090" {
		t.Fatalf("addr = %q", cfg.Server.Addr)
	}

	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("server: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("garbage yaml must fail")
	}
}

func TestEnvOverridesAddrOriginsAndDataDir(t *testing.T) {
	t.Setenv("CC_ADDR", "127.0.0.1:9999")
	t.Setenv("CC_ORIGINS", " https://example.test ,https://other.test")
	t.Setenv("CC_DATA_DIR", filepath.Join(t.TempDir(), "state"))

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "127.0.0.1:9999" {
		t.Fatalf("addr = %q", cfg.Server.Addr)
	}
	if len(cfg.Server.Origins) != 2 || cfg.Server.Origins[0] != "https://example.test" {
		t.Fatalf("origins = %#v", cfg.Server.Origins)
	}
	if !strings.HasSuffix(cfg.Data.DB, "control-center.db") {
		t.Fatalf("db = %q", cfg.Data.DB)
	}
	if !strings.HasSuffix(filepath.ToSlash(cfg.Data.BlobDir), "blobs") {
		t.Fatalf("blob dir = %q", cfg.Data.BlobDir)
	}
}

func TestNonLoopbackRequiresTLS(t *testing.T) {
	cfg := Default()
	cfg.Server.Addr = "0.0.0.0:8080"
	if err := cfg.Validate(); err == nil {
		t.Fatal("plain HTTP on a public address must fail")
	}

	cfg.Server.TLSCert = "cert.pem"
	if err := cfg.Validate(); err == nil {
		t.Fatal("tlsCert without tlsKey must fail")
	}
	cfg.Server.TLSKey = "key.pem"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.TLSEnabled() {
		t.Fatal("both cert and key should enable TLS")
	}
}

func TestEmptyHostIsNotLoopback(t *testing.T) {
	cfg := Default()
	cfg.Server.Addr = ":8080"
	if cfg.LoopbackOnly() {
		t.Fatal("binding every interface is not loopback-only")
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("an all-interfaces listener without TLS must fail")
	}
}

func TestIPv6LoopbackDoesNotNeedTLS(t *testing.T) {
	cfg := Default()
	cfg.Server.Addr = "[::1]:8080"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.LoopbackOnly() {
		t.Fatal("[::1] is loopback")
	}
}

func TestBlobLimitsMustBeFiniteAndOrdered(t *testing.T) {
	cfg := Default()
	cfg.Blobs.MaxObjectBytes = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("zero object limit must fail")
	}
	cfg.Blobs.MaxObjectBytes = 100
	cfg.Blobs.MaxScopeBytes = 50
	if err := cfg.Validate(); err == nil {
		t.Fatal("object limit above scope quota must fail")
	}
}

func TestDeriveFillsZeroDurationsAndPool(t *testing.T) {
	cfg := Config{Server: Server{Addr: "127.0.0.1:8080"}, Blobs: Default().Blobs}
	cfg.derive()
	if cfg.Data.BusyTimeout != 5*time.Second || cfg.Data.ReadPool != 8 {
		t.Fatalf("busy=%v pool=%d", cfg.Data.BusyTimeout, cfg.Data.ReadPool)
	}
	if cfg.Session.Absolute != 12*time.Hour || cfg.Session.Idle != time.Hour || cfg.Session.ReauthWindow != 5*time.Minute {
		t.Fatalf("session = %+v", cfg.Session)
	}
	// An idle window that is not shorter than the absolute one can never close first,
	// which would leave an abandoned session alive for as long as a used one.
	if cfg.Session.Idle >= cfg.Session.Absolute {
		t.Fatalf("idle %v does not expire before absolute %v", cfg.Session.Idle, cfg.Session.Absolute)
	}
	if cfg.AI.Models != "config/models.yaml" {
		t.Fatalf("ai.models = %q", cfg.AI.Models)
	}
	if cfg.Browser.Engine != "fake" {
		t.Fatalf("browser.engine = %q", cfg.Browser.Engine)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserEnginePlaywrightAndUnknown(t *testing.T) {
	cfg := Default()
	cfg.Browser.Engine = "playwright"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Browser.Engine = "chromedp"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown engine must fail")
	}
}

func TestSearchEngineSearxngRequiresURL(t *testing.T) {
	cfg := Default()
	if cfg.Search.Engine != "fake" {
		t.Fatalf("default search engine = %q", cfg.Search.Engine)
	}
	cfg.Search.Engine = "searxng"
	if err := cfg.Validate(); err == nil {
		t.Fatal("searxng without url must fail")
	}
	cfg.Search.SearXNG.URL = "http://searxng:8080"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Search.SearXNG.URL = "file:///etc/passwd"
	if err := cfg.Validate(); err == nil {
		t.Fatal("file url must fail")
	}
}

func TestSearchEnvOverrides(t *testing.T) {
	t.Setenv("CC_SEARCH_ENGINE", "searxng")
	t.Setenv("CC_SEARCH_SEARXNG_URL", "http://searxng:8080")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Search.Engine != "searxng" || cfg.Search.SearXNG.URL != "http://searxng:8080" {
		t.Fatalf("search = %+v", cfg.Search)
	}
}

func TestHarnessProfilesAreClosedAndBounded(t *testing.T) {
	cfg := Default()
	cfg.Harness.Profiles = []HarnessProfile{{
		ID: "codex", Command: "codex", Args: []string{"exec"},
		WorkspaceRoot: t.TempDir(), AcceptsInstruction: true,
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Harness.Profiles = append(cfg.Harness.Profiles, cfg.Harness.Profiles[0])
	if err := cfg.Validate(); err == nil {
		t.Fatal("duplicate harness profile must fail")
	}
	cfg = Default()
	cfg.Harness.MaxSessions = 33
	if err := cfg.Validate(); err == nil {
		t.Fatal("unbounded harness session count must fail")
	}
}
