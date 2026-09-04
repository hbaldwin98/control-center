// Package config loads the non-secret application configuration.
//
// Secrets never live here. API keys and OAuth credentials live in the credentials module,
// encrypted at rest with a key supplied through the environment or the OS keyring.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the whole application configuration.
type Config struct {
	Server  Server  `yaml:"server"`
	Data    Data    `yaml:"data"`
	Session Session `yaml:"session"`
	Blobs   Blobs   `yaml:"blobs"`
	AI      AI      `yaml:"ai"`
	Browser Browser `yaml:"browser"`
	Search  Search  `yaml:"search"`
	Harness Harness `yaml:"harness"`
}

// Server describes the HTTP listener.
type Server struct {
	// Addr is the listen address. A loopback address may serve plain HTTP; any other
	// address requires TLS.
	Addr string `yaml:"addr"`

	// TLSCert and TLSKey are PEM file paths. Both or neither.
	TLSCert string `yaml:"tlsCert"`
	TLSKey  string `yaml:"tlsKey"`

	// Origins lists additional acceptable Origin header values for mutations, beyond the
	// one implied by Addr. Use this when serving behind a name that differs from Addr.
	Origins []string `yaml:"origins"`

	// TrustedProxy says a reverse proxy terminates connections in front of this server
	// and that its X-Forwarded-* headers may be believed.
	//
	// Set it only when something you control is the *only* way to reach the listener,
	// because it changes who the server thinks it is talking to. With it set, the peer
	// address for rate limiting is read from X-Forwarded-For instead of the connection,
	// no peer counts as loopback (a proxied request is never the operator at the
	// console, however local the socket looks), and X-Forwarded-Proto decides whether
	// the response carries HSTS.
	TrustedProxy bool `yaml:"trustedProxy"`
}

// Data describes where persistent state lives.
type Data struct {
	Dir string `yaml:"dir"`

	// DB and BlobDir default to paths under Dir.
	DB      string `yaml:"db"`
	BlobDir string `yaml:"blobDir"`

	BusyTimeout time.Duration `yaml:"busyTimeout"`
	ReadPool    int           `yaml:"readPool"`
}

// Session describes the administrator session policy.
type Session struct {
	// Absolute is the longest a session may live, however active it is.
	Absolute time.Duration `yaml:"absolute"`

	// Idle is how long a session survives without a request. It must be shorter than
	// Absolute to mean anything: set equal, an abandoned session lasts exactly as long
	// as a used one, and a stolen cookie keeps its full value until the day is out.
	Idle time.Duration `yaml:"idle"`

	// ReauthWindow is how long a password reauthentication stays valid for credential
	// changes.
	ReauthWindow time.Duration `yaml:"reauthWindow"`
}

// AI is host-managed model routing. Secrets stay in credentials, not here.
type AI struct {
	Models string `yaml:"models"`
}

// Browser selects the headless engine. Plugins never choose this.
type Browser struct {
	// Engine is "fake" (in-process, no sockets) or "playwright" (Chromium). Empty
	// defaults to fake.
	Engine string `yaml:"engine"`
	Reader Reader `yaml:"reader"`
}

type Reader struct {
	URL string `yaml:"url"`
}

// Search selects the web-lookup engine. Plugins never choose this or see the URL.
type Search struct {
	// Engine is "fake" (in-process fixtures) or "searxng" (sidecar JSON API). Empty
	// defaults to fake.
	Engine string `yaml:"engine"`
	// SearXNG is the private instance the host queries. Required when Engine is searxng.
	SearXNG SearXNG `yaml:"searxng"`
}

// SearXNG is the sidecar the host calls. Result pages stay on the public web; this
// URL is only the search engine itself.
type SearXNG struct {
	URL string `yaml:"url"`
}

// Harness describes the administrator-approved coding-agent commands. A browser request
// may select a profile, but cannot change its executable, arguments, or workspace root.
type Harness struct {
	MaxSessions    int              `yaml:"maxSessions"`
	MaxOutputBytes int64            `yaml:"maxOutputBytes"`
	StopTimeout    time.Duration    `yaml:"stopTimeout"`
	Profiles       []HarnessProfile `yaml:"profiles"`
}

type HarnessProfile struct {
	ID                 string   `yaml:"id"`
	Name               string   `yaml:"name"`
	Command            string   `yaml:"command"`
	Args               []string `yaml:"args"`
	WorkspaceRoot      string   `yaml:"workspaceRoot"`
	AcceptsInstruction bool     `yaml:"acceptsInstruction"`
}

// Blobs bounds filesystem blob storage. Limits must be finite.
type Blobs struct {
	MaxObjectBytes int64 `yaml:"maxObjectBytes"`
	MaxScopeBytes  int64 `yaml:"maxScopeBytes"`
}

// Default returns the built-in configuration: loopback only, data under ./data.
func Default() Config {
	return Config{
		Server: Server{Addr: "127.0.0.1:8080"},
		Data: Data{
			Dir:         "data",
			BusyTimeout: 5 * time.Second,
			ReadPool:    8,
		},
		Session: Session{
			Absolute:     12 * time.Hour,
			Idle:         time.Hour,
			ReauthWindow: 5 * time.Minute,
		},
		Blobs: Blobs{
			MaxObjectBytes: 64 << 20, // 64 MiB
			MaxScopeBytes:  2 << 30,  // 2 GiB
		},
		AI:      AI{Models: "config/models.yaml"},
		Browser: Browser{Engine: "fake"},
		Search:  Search{Engine: "fake"},
		Harness: Harness{MaxSessions: 4, MaxOutputBytes: 1 << 20, StopTimeout: 5 * time.Second},
	}
}

// Load reads path if it exists, layers environment overrides on top, fills in derived
// defaults, and validates the result. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return cfg, fmt.Errorf("config: parse %s: %w", path, err)
			}
		case errors.Is(err, os.ErrNotExist):
			// Defaults are a usable loopback configuration.
		default:
			return cfg, fmt.Errorf("config: read %s: %w", path, err)
		}
	}
	cfg.applyEnv()
	cfg.derive()
	return cfg, cfg.Validate()
}

func (c *Config) applyEnv() {
	if v := os.Getenv("CC_ADDR"); v != "" {
		c.Server.Addr = v
	}
	if v := os.Getenv("CC_TLS_CERT"); v != "" {
		c.Server.TLSCert = v
	}
	if v := os.Getenv("CC_TLS_KEY"); v != "" {
		c.Server.TLSKey = v
	}
	if v := os.Getenv("CC_DATA_DIR"); v != "" {
		c.Data.Dir = v
		c.Data.DB = ""
		c.Data.BlobDir = ""
	}
	if v := os.Getenv("CC_BROWSER_ENGINE"); v != "" {
		c.Browser.Engine = v
	}
	if v := os.Getenv("CC_BROWSER_READER_URL"); v != "" {
		c.Browser.Reader.URL = v
	}
	if v := os.Getenv("CC_SEARCH_ENGINE"); v != "" {
		c.Search.Engine = v
	}
	if v := os.Getenv("CC_SEARCH_SEARXNG_URL"); v != "" {
		c.Search.SearXNG.URL = v
	}
	if v := os.Getenv("CC_TRUSTED_PROXY"); v != "" {
		c.Server.TrustedProxy = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("CC_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.Server.Origins = append(c.Server.Origins, o)
			}
		}
	}
}

func (c *Config) derive() {
	if c.Data.Dir == "" {
		c.Data.Dir = "data"
	}
	if c.Data.DB == "" {
		c.Data.DB = filepath.Join(c.Data.Dir, "control-center.db")
	}
	if c.Data.BlobDir == "" {
		c.Data.BlobDir = filepath.Join(c.Data.Dir, "blobs")
	}
	if c.Data.BusyTimeout <= 0 {
		c.Data.BusyTimeout = 5 * time.Second
	}
	if c.Data.ReadPool <= 0 {
		c.Data.ReadPool = 8
	}
	if c.Session.Absolute <= 0 {
		c.Session.Absolute = 12 * time.Hour
	}
	if c.Session.Idle <= 0 {
		c.Session.Idle = time.Hour
	}
	if c.Session.ReauthWindow <= 0 {
		c.Session.ReauthWindow = 5 * time.Minute
	}
	if c.AI.Models == "" {
		c.AI.Models = "config/models.yaml"
	}
	if c.Browser.Engine == "" {
		c.Browser.Engine = "fake"
	}
	if c.Search.Engine == "" {
		c.Search.Engine = "fake"
	}
	if c.Harness.MaxSessions <= 0 {
		c.Harness.MaxSessions = 4
	}
	if c.Harness.MaxOutputBytes <= 0 {
		c.Harness.MaxOutputBytes = 1 << 20
	}
	if c.Harness.StopTimeout <= 0 {
		c.Harness.StopTimeout = 5 * time.Second
	}
	for i := range c.Harness.Profiles {
		if c.Harness.Profiles[i].Name == "" {
			c.Harness.Profiles[i].Name = c.Harness.Profiles[i].ID
		}
	}
}

// Validate enforces the invariants the rest of the system relies on.
func (c Config) Validate() error {
	host, _, err := net.SplitHostPort(c.Server.Addr)
	if err != nil {
		return fmt.Errorf("config: server.addr %q: %w", c.Server.Addr, err)
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		return errors.New("config: server.tlsCert and server.tlsKey must be set together")
	}
	if !c.TLSEnabled() && !isLoopbackHost(host) {
		return fmt.Errorf("config: %q is not loopback, so TLS is required", c.Server.Addr)
	}
	if c.Blobs.MaxObjectBytes <= 0 || c.Blobs.MaxScopeBytes <= 0 {
		return errors.New("config: blob limits must be finite and positive")
	}
	if c.Blobs.MaxObjectBytes > c.Blobs.MaxScopeBytes {
		return errors.New("config: blobs.maxObjectBytes exceeds blobs.maxScopeBytes")
	}
	switch strings.ToLower(c.Browser.Engine) {
	case "fake", "playwright":
	default:
		return fmt.Errorf("config: browser.engine %q is not supported (fake or playwright)", c.Browser.Engine)
	}
	if c.Browser.Reader.URL != "" {
		if err := validateSidecarURL("browser.reader.url", c.Browser.Reader.URL); err != nil {
			return err
		}
	}
	switch strings.ToLower(c.Search.Engine) {
	case "fake":
	case "searxng":
		if err := validateSearxngURL(c.Search.SearXNG.URL); err != nil {
			return err
		}
	default:
		return fmt.Errorf("config: search.engine %q is not supported (fake or searxng)", c.Search.Engine)
	}
	if c.Harness.MaxSessions < 1 || c.Harness.MaxSessions > 32 {
		return errors.New("config: harness.maxSessions must be 1..32")
	}
	if c.Harness.MaxOutputBytes < 4096 || c.Harness.MaxOutputBytes > 64<<20 {
		return errors.New("config: harness.maxOutputBytes must be 4096..67108864")
	}
	if c.Harness.StopTimeout <= 0 || c.Harness.StopTimeout > time.Minute {
		return errors.New("config: harness.stopTimeout must be positive and at most 1m")
	}
	profileIDs := make(map[string]struct{}, len(c.Harness.Profiles))
	for _, p := range c.Harness.Profiles {
		if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Command) == "" || strings.TrimSpace(p.WorkspaceRoot) == "" {
			return fmt.Errorf("config: harness profile %q requires id, command, and workspaceRoot", p.ID)
		}
		if _, exists := profileIDs[p.ID]; exists {
			return fmt.Errorf("config: duplicate harness profile %q", p.ID)
		}
		profileIDs[p.ID] = struct{}{}
	}
	return nil
}

// TLSEnabled reports whether the server should terminate TLS itself.
func (c Config) TLSEnabled() bool { return c.Server.TLSCert != "" && c.Server.TLSKey != "" }

// LoopbackOnly reports whether the listener is reachable only from this machine. First-run
// bootstrap is offered only when a request also arrives from a loopback peer.
func (c Config) LoopbackOnly() bool {
	host, _, err := net.SplitHostPort(c.Server.Addr)
	if err != nil {
		return false
	}
	return isLoopbackHost(host)
}

func validateSearxngURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("config: search.searxng.url is required when search.engine is searxng")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("config: search.searxng.url %q must be an http(s) URL with a host", raw)
	}
	return nil
}

func validateSidecarURL(name, raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("config: %s %q must be an http(s) base URL with a host", name, raw)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "" {
		// An empty host means every interface.
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
