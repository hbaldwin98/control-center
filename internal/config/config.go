// Package config loads the non-secret application configuration.
//
// Secrets never live here. API keys and OAuth credentials live in the credentials module,
// encrypted at rest with a key supplied through the environment or the OS keyring.
package config

import (
	"errors"
	"fmt"
	"net"
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
	Absolute time.Duration `yaml:"absolute"`
	Idle     time.Duration `yaml:"idle"`

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
			Idle:         12 * time.Hour,
			ReauthWindow: 5 * time.Minute,
		},
		Blobs: Blobs{
			MaxObjectBytes: 64 << 20, // 64 MiB
			MaxScopeBytes:  2 << 30,  // 2 GiB
		},
		AI:      AI{Models: "config/models.yaml"},
		Browser: Browser{Engine: "fake"},
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
		c.Session.Idle = 12 * time.Hour
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
